import { signRequest, hmac } from "./signature.js";
import { encodeMetadata } from "./metadata.js";
const encoder = new TextEncoder();
function record(value, allowed) {
  if (
    !value ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.keys(value).some((key) => !allowed.includes(key))
  ) {
    throw new TypeError("Unsupported R2 option");
  }
}
function logicalKey(key, allowEmpty = false) {
  if (
    typeof key !== "string" ||
    key.length > 1024 ||
    (!allowEmpty && !key) ||
    encoder.encode(key).length > 1024 ||
    /[\u0000-\u001f\u007f\\]/.test(key) ||
    key.split("/").some((part) => part === "." || part === "..")
  ) {
    throw new TypeError("Invalid or unsupported object key");
  }
}
function conditions(value, write = false) {
  if (value === undefined) {
    return {};
  }
  if (value instanceof Headers) {
    const map = {
      "if-match": "etagMatches",
      "if-none-match": "etagDoesNotMatch",
      "if-modified-since": "uploadedAfter",
      "if-unmodified-since": "uploadedBefore",
    };
    value = Object.fromEntries(
      Object.entries(map)
        .filter(([name]) => value.has(name))
        .map(([name, key]) => [key, value.get(name)]),
    );
  }
  record(value, [
    "etagMatches",
    "etagDoesNotMatch",
    "uploadedBefore",
    "uploadedAfter",
    "secondsGranularity",
  ]);
  if (Object.keys(value).filter((key) => key !== "secondsGranularity").length > 1) {
    throw new TypeError("Combined conditions are not supported by this profile");
  }
  if (value.secondsGranularity !== undefined && typeof value.secondsGranularity !== "boolean") {
    throw new TypeError("Invalid secondsGranularity");
  }
  const headers = {};
  for (const [key, header] of [
    ["etagMatches", "if-match"],
    ["etagDoesNotMatch", "if-none-match"],
  ]) {
    if (value[key] === undefined) {
      continue;
    }
    if (typeof value[key] !== "string" || /[\r\n,]/.test(value[key])) {
      throw new TypeError("Invalid ETag condition");
    }
    if (write && key === "etagDoesNotMatch" && value[key] !== "*") {
      throw new TypeError("Unsupported conditional write");
    }
    headers[header] =
      value[key] === "*" || value[key].startsWith('"') ? value[key] : '"' + value[key] + '"';
  }
  for (const [key, header] of [
    ["uploadedBefore", "if-unmodified-since"],
    ["uploadedAfter", "if-modified-since"],
  ]) {
    if (value[key] === undefined) {
      continue;
    }
    if (write) {
      throw new TypeError("Date conditions on writes are not supported");
    }
    const date = new Date(value[key]);
    if (!Number.isFinite(date.valueOf())) {
      throw new TypeError("Invalid date condition");
    }
    if (date.valueOf() % 1000 !== 0 && !value.secondsGranularity) {
      throw new TypeError("Subsecond date conditions are not supported");
    }
    headers[header] = date.toUTCString();
  }
  return headers;
}
function rangeHeader(range) {
  if (range === undefined) {
    return {};
  }
  record(range, ["offset", "length", "suffix"]);
  for (const value of Object.values(range)) {
    if (!Number.isSafeInteger(value) || value < 0) {
      throw new TypeError("Invalid range");
    }
  }
  if (range.suffix !== undefined) {
    if (!range.suffix || range.offset !== undefined || range.length !== undefined) {
      throw new TypeError("Invalid suffix range");
    }
    return { range: "bytes=-" + range.suffix };
  }
  const offset = range.offset ?? 0;
  if (range.length === 0 || !Number.isSafeInteger(offset + (range.length ?? 0))) {
    throw new TypeError("Invalid range length");
  }
  return {
    range: "bytes=" + offset + "-" + (range.length === undefined ? "" : offset + range.length - 1),
  };
}
function listingOptions(options) {
  record(options, ["prefix", "delimiter", "cursor", "limit", "include", "startAfter"]);
  logicalKey(options.prefix ?? "", true);
  if (options.delimiter !== undefined && options.delimiter !== "" && options.delimiter !== "/") {
    throw new TypeError("Only slash delimiters are supported");
  }
  if (options.startAfter !== undefined) {
    throw new TypeError("startAfter is not supported by this profile");
  }
  if (
    options.limit !== undefined &&
    (!Number.isInteger(options.limit) || options.limit < 1 || options.limit > 1000)
  ) {
    throw new TypeError("Invalid list limit");
  }
  if (
    options.include !== undefined &&
    (!Array.isArray(options.include) ||
      options.include.some((v) => !["httpMetadata", "customMetadata"].includes(v)))
  ) {
    throw new TypeError("Invalid list include");
  }
  return {
    prefix: options.prefix ?? "",
    delimiter: options.delimiter ?? "",
    include: [...new Set(options.include ?? [])].sort(),
  };
}
const b64 = (value) =>
  btoa(String.fromCharCode(...new Uint8Array(value)))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
const unb64 = (value) =>
  Uint8Array.from(atob(value.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
export async function createCursor(grant, token, options) {
  const params = listingOptions(options);
  if (!grant.cursor_key || typeof token !== "string" || !token || token.length > 8192) {
    throw new Error("Storage cursor unavailable");
  }
  const payload = b64(
    encoder.encode(
      JSON.stringify({ bucket: grant.bucket_id, params, token, expires: Date.now() + 3600000 }),
    ),
  );
  return payload + "." + b64(await hmac(grant.cursor_key, payload));
}

export async function createMultipartToken(grant, key, uploadId, version) {
  logicalKey(key);
  if (
    typeof uploadId !== "string" ||
    !uploadId ||
    uploadId.length > 2048 ||
    typeof version !== "string" ||
    !/^[a-f0-9-]{36}$/.test(version) ||
    !grant.cursor_key
  ) {
    throw new TypeError("Invalid multipart identity");
  }
  const payload = b64(
    encoder.encode(
      JSON.stringify({
        bucket: grant.bucket_id,
        store: grant.store_id,
        key,
        uploadId,
        version,
        expires: Date.now() + 7 * 86400000,
      }),
    ),
  );
  return payload + "." + b64(await hmac(grant.cursor_key, "multipart\0" + payload));
}

async function openMultipart(grant, key, token) {
  if (typeof token !== "string" || token.length > 8192 || !grant.cursor_key) {
    throw new TypeError("Invalid multipart uploadId");
  }
  const parts = token.split(".");
  if (parts.length !== 2) {
    throw new TypeError("Invalid multipart uploadId");
  }
  const expected = new Uint8Array(await hmac(grant.cursor_key, "multipart\0" + parts[0]));
  let signature;
  let payload;
  try {
    signature = unb64(parts[1]);
    payload = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(unb64(parts[0])));
  } catch {
    throw new TypeError("Invalid multipart uploadId");
  }
  let difference = expected.length ^ signature.length;
  for (let index = 0; index < expected.length; index++) {
    difference |= expected[index] ^ (signature[index] ?? 0);
  }
  if (
    difference ||
    payload.bucket !== grant.bucket_id ||
    payload.store !== grant.store_id ||
    payload.key !== key ||
    !Number.isFinite(payload.expires) ||
    payload.expires <= Date.now() ||
    typeof payload.uploadId !== "string" ||
    !payload.uploadId ||
    payload.uploadId.length > 2048 ||
    typeof payload.version !== "string" ||
    !/^[a-f0-9-]{36}$/.test(payload.version)
  ) {
    throw new TypeError("Multipart scope or expiry mismatch");
  }
  return payload;
}
async function openCursor(grant, cursor, params) {
  if (typeof cursor !== "string" || cursor.length > 16384 || !grant.cursor_key) {
    throw new TypeError("Invalid list cursor");
  }
  const parts = cursor.split(".");
  if (parts.length !== 2) {
    throw new TypeError("Invalid list cursor");
  }
  const expected = new Uint8Array(await hmac(grant.cursor_key, parts[0]));
  let signature;
  try {
    signature = unb64(parts[1]);
  } catch {
    throw new TypeError("Invalid list cursor");
  }
  let difference = expected.length ^ signature.length;
  for (let index = 0; index < expected.length; index++) {
    difference |= expected[index] ^ (signature[index] ?? 0);
  }
  if (difference) {
    throw new TypeError("Invalid list cursor");
  }
  let payload;
  try {
    payload = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(unb64(parts[0])));
  } catch {
    throw new TypeError("Invalid list cursor");
  }
  if (
    payload.bucket !== grant.bucket_id ||
    payload.expires < Date.now() ||
    JSON.stringify(payload.params) !== JSON.stringify(params)
  ) {
    throw new TypeError("List cursor scope or expiry mismatch");
  }
  return payload.token;
}

export async function prepareStorageRequest(grant, operation, key, options = {}) {
  logicalKey(key, operation === "list");
  if (encoder.encode(grant.prefix + key).length > 1024) {
    throw new TypeError("Physical object key exceeds storage profile limit");
  }
  let method;
  let headers = {};
  let query = {};
  let version;
  if (operation === "list") {
    const params = listingOptions(options);
    method = "GET";
    query = {
      "list-type": "2",
      prefix: grant.prefix + params.prefix,
      "max-keys": Math.min(options.limit ?? 64, 64),
    };
    if (params.delimiter) {
      query.delimiter = params.delimiter;
    }
    if (options.cursor) {
      query["continuation-token"] = await openCursor(grant, options.cursor, params);
    }
  } else if (operation === "head" || operation === "get") {
    record(options, ["onlyIf", "range"]);
    method = operation === "head" ? "HEAD" : "GET";
    // Preserve stored bytes even when HTTP metadata says gzip/br. The R2
    // envelope retains the original contentEncoding for writeHttpMetadata().
    if (operation === "get") {
      query["response-content-encoding"] = "identity";
    }
    headers = { ...conditions(options.onlyIf), ...rangeHeader(options.range) };
  } else if (operation === "delete") {
    record(options, []);
    method = "DELETE";
  } else if (operation === "put" || operation === "multipart-create") {
    record(
      options,
      operation === "put"
        ? ["onlyIf", "httpMetadata", "customMetadata", "storageClass"]
        : ["httpMetadata", "customMetadata", "storageClass"],
    );
    if (options.storageClass !== undefined && options.storageClass !== "Standard") {
      throw new TypeError("Unsupported storage class");
    }
    method = operation === "put" ? "PUT" : "POST";
    if (operation === "multipart-create") {
      query.uploads = "";
    }
    version = crypto.randomUUID();
    headers = {
      ...conditions(options.onlyIf, true),
      ...encodeMetadata(version, options),
      "x-amz-storage-class": "STANDARD",
    };
  } else if (
    ["multipart-part", "multipart-complete", "multipart-abort", "multipart-list"].includes(
      operation,
    )
  ) {
    record(options, [
      "uploadId",
      ...(operation === "multipart-part"
        ? ["partNumber"]
        : operation === "multipart-list"
          ? ["marker"]
          : []),
    ]);
    const upload = await openMultipart(grant, key, options.uploadId);
    version = upload.version;
    query.uploadId = upload.uploadId;
    if (operation === "multipart-part") {
      if (
        !Number.isInteger(options.partNumber) ||
        options.partNumber < 1 ||
        options.partNumber > 10000
      ) {
        throw new TypeError("Invalid multipart part number");
      }
      query.partNumber = options.partNumber;
      method = "PUT";
    } else if (operation === "multipart-complete") {
      method = "POST";
      headers["content-type"] = "application/xml";
    } else if (operation === "multipart-abort") {
      method = "DELETE";
    } else {
      method = "GET";
      query["max-parts"] = 1000;
      if (options.marker !== undefined) {
        if (!Number.isInteger(options.marker) || options.marker < 0 || options.marker > 10000) {
          throw new TypeError("Invalid multipart part marker");
        }
        query["part-number-marker"] = options.marker;
      }
    }
  } else {
    throw new TypeError("Unsupported storage operation");
  }
  const signed = await signRequest(
    grant,
    method,
    operation === "list" ? "" : grant.prefix + key,
    query,
    headers,
  );
  return {
    ...signed,
    version,
    prefix: grant.prefix,
    timeoutMs: ["get", "put", "multipart-part", "multipart-complete"].includes(operation)
      ? (grant.request_timeout_ms ?? 600000)
      : 30000,
  };
}
