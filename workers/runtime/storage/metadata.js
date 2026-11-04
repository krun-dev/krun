const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true });
export const httpFields = {
  contentType: "content-type",
  contentLanguage: "content-language",
  contentDisposition: "content-disposition",
  contentEncoding: "content-encoding",
  cacheControl: "cache-control",
  cacheExpiry: "expires",
};
export function encodeMetadata(version, options = {}) {
  const customMetadata = options.customMetadata ?? {};
  if (
    !customMetadata ||
    typeof customMetadata !== "object" ||
    Array.isArray(customMetadata) ||
    Object.values(customMetadata).some((v) => typeof v !== "string")
  ) {
    throw new TypeError("Invalid customMetadata");
  }
  if (
    Object.keys(customMetadata).length > 64 ||
    Object.entries(customMetadata).some(([key, value]) => key.length > 1800 || value.length > 1800)
  ) {
    throw new TypeError("Metadata exceeds the storage profile budget");
  }
  const httpMetadata = {};
  if (options.httpMetadata instanceof Headers) {
    for (const [name, header] of Object.entries(httpFields)) {
      if (options.httpMetadata.has(header)) {
        httpMetadata[name] = options.httpMetadata.get(header);
      }
    }
  } else if (options.httpMetadata !== undefined) {
    if (
      !options.httpMetadata ||
      typeof options.httpMetadata !== "object" ||
      Array.isArray(options.httpMetadata)
    ) {
      throw new TypeError("Invalid httpMetadata");
    }
    for (const [name, value] of Object.entries(options.httpMetadata)) {
      if (!Object.hasOwn(httpFields, name)) {
        throw new TypeError("Unsupported HTTP metadata field");
      }
      if (name === "cacheExpiry") {
        const date = new Date(value);
        if (!Number.isFinite(date.valueOf())) {
          throw new TypeError("Invalid cacheExpiry");
        }
        httpMetadata[name] = date.toISOString();
      } else {
        if (typeof value !== "string" || value.length > 1800) {
          throw new TypeError("Invalid HTTP metadata value");
        }
        httpMetadata[name] = value;
      }
    }
  }
  const serialized = JSON.stringify({ version, customMetadata, httpMetadata });
  if (serialized.length > 1350) {
    throw new TypeError("Metadata exceeds the storage profile budget");
  }
  const raw = encoder.encode(serialized);
  const envelope = btoa(String.fromCharCode(...raw));
  if (envelope.length > 1800) {
    throw new TypeError("Metadata exceeds the storage profile budget");
  }
  const headers = { "x-amz-meta-krun-v1": envelope };
  for (const [name, value] of Object.entries(httpMetadata)) {
    headers[httpFields[name]] = name === "cacheExpiry" ? new Date(value).toUTCString() : value;
  }
  return headers;
}
export function objectMetadata(key, response, body = false, abort = () => {}) {
  const encoded = response.headers.get("x-amz-meta-krun-v1");
  if (!encoded || encoded.length > 1800) {
    throw new Error("Object does not contain valid R2 metadata");
  }
  let stored;
  try {
    stored = JSON.parse(
      decoder.decode(Uint8Array.from(atob(encoded || ""), (c) => c.charCodeAt(0))),
    );
  } catch {
    throw new Error("Object does not contain valid R2 metadata");
  }
  const modified = response.headers.get("last-modified");
  const sizeText =
    response.headers.get("content-range")?.split("/")[1] ?? response.headers.get("content-length");
  const uploaded = new Date(modified);
  if (!modified || !/^(0|[1-9][0-9]*)$/.test(sizeText ?? "")) {
    throw new Error("Invalid object metadata");
  }
  const size = Number(sizeText);
  const httpEtag = response.headers.get("etag");
  if (
    !stored ||
    typeof stored !== "object" ||
    typeof stored.version !== "string" ||
    !stored.version ||
    !httpEtag ||
    !Number.isFinite(uploaded.valueOf()) ||
    !Number.isSafeInteger(size) ||
    size < 0
  ) {
    throw new Error("Invalid object metadata");
  }
  if (
    !stored.customMetadata ||
    Array.isArray(stored.customMetadata) ||
    typeof stored.customMetadata !== "object" ||
    Object.values(stored.customMetadata).some((value) => typeof value !== "string") ||
    !stored.httpMetadata ||
    Array.isArray(stored.httpMetadata) ||
    typeof stored.httpMetadata !== "object" ||
    Object.entries(stored.httpMetadata).some(
      ([name, value]) => !Object.hasOwn(httpFields, name) || typeof value !== "string",
    )
  ) {
    throw new Error("Invalid object metadata");
  }
  const storageClass = response.headers.get("x-amz-storage-class");
  if (storageClass && storageClass !== "STANDARD") {
    throw new Error("Unsupported object storage class");
  }
  const httpMetadata = { ...stored.httpMetadata };
  if (Object.hasOwn(httpMetadata, "cacheExpiry")) {
    httpMetadata.cacheExpiry = new Date(httpMetadata.cacheExpiry);
    if (!Number.isFinite(httpMetadata.cacheExpiry.valueOf())) {
      throw new Error("Invalid object metadata");
    }
  }
  const object = {
    key,
    version: stored.version,
    size,
    etag: httpEtag.replace(/^"|"$/g, ""),
    httpEtag,
    uploaded,
    httpMetadata,
    customMetadata: stored.customMetadata,
    checksums: {
      toJSON() {
        return {};
      },
    },
    storageClass: "Standard",
    writeHttpMetadata(headers) {
      for (const [name, value] of Object.entries(httpMetadata)) {
        headers.set(httpFields[name], value instanceof Date ? value.toUTCString() : value);
      }
    },
  };
  if (response.status === 206) {
    const match = response.headers.get("content-range")?.match(/^bytes (\d+)-(\d+)\/(\d+)$/);
    if (!match) {
      throw new Error("Invalid storage range response");
    }
    object.range = { offset: Number(match[1]), length: Number(match[2]) - Number(match[1]) + 1 };
  }
  if (body) {
    const encoding = response.headers.get("content-encoding");
    if (encoding && encoding.toLowerCase() !== "identity") {
      throw new Error("Storage did not preserve raw object bytes");
    }
    const source =
      response.body ??
      new ReadableStream({
        start(controller) {
          controller.close();
        },
      });
    const upstream = source.getReader();
    const stream = new ReadableStream({
      async pull(controller) {
        try {
          const result = await upstream.read();
          if (result.done) {
            controller.close();
          } else {
            controller.enqueue(result.value);
          }
        } catch (error) {
          controller.error(error);
        }
      },
      async cancel(reason) {
        abort();
        await upstream.cancel(reason).catch(() => {});
      },
    });
    const reader = new Response(stream, {
      headers: httpMetadata.contentType ? { "content-type": httpMetadata.contentType } : {},
    });
    Object.assign(object, {
      body: reader.body,
      arrayBuffer: () => reader.arrayBuffer(),
      text: () => reader.text(),
      json: () => reader.json(),
      blob: () => reader.blob(),
    });
    Object.defineProperty(object, "bodyUsed", { get: () => reader.bodyUsed });
  }
  return object;
}
