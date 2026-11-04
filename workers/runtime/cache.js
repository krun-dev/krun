// Pod-local Cache API storage. Keep only completed plain data across requests.
const entries = new Map();
const index = new Map();
const maxBytes = 16 * 1024 * 1024;
const maxEntryBytes = 4 * 1024 * 1024;
const maxEntries = 1024;
let storedBytes = 0;
let activeWrites = 0;
const encoder = new TextEncoder();

function drop(key) {
  const entry = entries.get(key);
  if (!entry) {
    return;
  }
  entries.delete(key);
  storedBytes -= entry.weight;
  const keys = index.get(entry.base);
  keys.delete(key);
  if (!keys.size) {
    index.delete(entry.base);
  }
}

function ttl(headers, status, now) {
  let control = headers.get("cache-control") || "";
  if (/\bprivate\s*=\s*"?set-cookie"?(?:\s*,|\s*$)/i.test(control)) {
    headers.delete("set-cookie");
    control = control.replace(/(?:^|,)\s*private\s*=\s*"?set-cookie"?/gi, "");
    headers.set("cache-control", control.replace(/^\s*,/, "").trim());
  }
  if (
    headers.has("set-cookie") ||
    /(?:^|,)\s*(?:no-store|no-cache|private)(?:\s*[,=]|\s*$)/i.test(control)
  ) {
    return 0;
  }
  const directive = (name) =>
    control.match(
      new RegExp("(?:^|,)\\s*" + name + '\\s*=\\s*"?(\\d+)"?(?:\\s*,|\\s*$)', "i"),
    )?.[1];
  const age = Math.max(0, Number(headers.get("age")) || 0);
  const date = Date.parse(headers.get("date"));
  const elapsed = Math.max(age * 1000, Number.isFinite(date) ? now - date : 0, 0);
  const maxAge = directive("s-maxage") ?? directive("max-age");
  if (maxAge !== undefined) {
    return Math.max(0, Math.min(Number(maxAge), 31536000) * 1000 - elapsed);
  }
  const expires = Date.parse(headers.get("expires"));
  if (headers.has("expires")) {
    return Number.isFinite(expires)
      ? Math.max(0, expires - (Number.isFinite(date) ? date : now) - elapsed)
      : 0;
  }
  const seconds = [200, 301].includes(status)
    ? 7200
    : [302, 303].includes(status)
      ? 1200
      : [404, 410].includes(status)
        ? 180
        : 0;
  return Math.max(0, seconds * 1000 - elapsed);
}

function parseMetadata(request) {
  const encoded = request.headers.get("x-krun-cache");
  if (!encoded || encoded.length > 32768) {
    throw new TypeError("Invalid cache metadata");
  }
  const metadata = JSON.parse(
    new TextDecoder("utf-8", { fatal: true }).decode(
      Uint8Array.from(atob(encoded), (character) => character.charCodeAt(0)),
    ),
  );
  if (
    typeof metadata.name !== "string" ||
    metadata.name.length > 1025 ||
    typeof metadata.url !== "string" ||
    metadata.url.length > 8192 ||
    !Array.isArray(metadata.requestHeaders) ||
    metadata.requestHeaders.length > 100
  ) {
    throw new TypeError("Invalid cache metadata");
  }
  const url = new URL(metadata.url);
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) {
    throw new TypeError("Invalid cache URL");
  }
  url.hash = "";
  return { ...metadata, url: url.href, requestHeaders: new Headers(metadata.requestHeaders) };
}

function matchedResponse(entry, requestHeaders, now) {
  const headers = new Headers(entry.headers);
  headers.delete("cache-tag");
  headers.set("age", String(entry.age + Math.floor((now - entry.created) / 1000)));
  const noneMatch = requestHeaders.get("if-none-match");
  const current = headers.get("etag")?.replace(/^W\//, "");
  const modified = Date.parse(headers.get("last-modified"));
  const since = Date.parse(requestHeaders.get("if-modified-since"));
  if (
    (noneMatch &&
      (noneMatch.trim() === "*" ||
        (current &&
          noneMatch.split(",").some((value) => value.trim().replace(/^W\//, "") === current)))) ||
    (!noneMatch && Number.isFinite(modified) && Number.isFinite(since) && modified <= since)
  ) {
    headers.delete("content-length");
    return new Response(null, { status: 304, headers });
  }
  const range = requestHeaders.get("range");
  if (
    range &&
    headers.has("content-length") &&
    Number(headers.get("content-length")) === entry.bytes.byteLength
  ) {
    const match = range.match(/^bytes=(\d*)-(\d*)$/);
    if (match && (match[1] || match[2])) {
      const length = entry.bytes.byteLength;
      const start = match[1] ? Number(match[1]) : Math.max(0, length - Number(match[2]));
      const end = match[1] && match[2] ? Math.min(Number(match[2]), length - 1) : length - 1;
      if (
        !Number.isSafeInteger(start) ||
        !Number.isSafeInteger(end) ||
        start > end ||
        start >= length
      ) {
        headers.set("content-range", "bytes */" + length);
        headers.delete("content-length");
        return new Response(null, { status: 416, headers });
      }
      headers.set("content-range", "bytes " + start + "-" + end + "/" + length);
      headers.set("content-length", String(end - start + 1));
      return new Response(entry.bytes.subarray(start, end + 1), {
        status: 206,
        headers,
        encodeBody: "manual",
      });
    }
  }
  return new Response([204, 205, 304].includes(entry.status) ? null : entry.bytes, {
    status: entry.status,
    statusText: entry.statusText,
    headers,
    encodeBody: "manual",
  });
}

export async function cacheRequest(namespace, request) {
  const miss = () => new Response(null, { status: 504 });
  let metadata;
  try {
    metadata = parseMetadata(request);
  } catch {
    request.body?.cancel().catch(() => {});
    return new Response(null, { status: 400 });
  }
  const base = JSON.stringify([namespace, metadata.name, metadata.url]);
  const now = Date.now();
  if (request.method === "DELETE") {
    let found = false;
    for (const key of [...(index.get(base) || [])]) {
      const entry = entries.get(key);
      found ||= entry.expires > now;
      drop(key);
    }
    return new Response(null, { status: found ? 200 : 404 });
  }
  if (request.method === "GET") {
    for (const key of [...(index.get(base) || [])].reverse()) {
      const entry = entries.get(key);
      if (entry.expires <= now) {
        drop(key);
        continue;
      }
      if (entry.vary.some(([name, value]) => metadata.requestHeaders.get(name) !== value)) {
        continue;
      }
      entries.delete(key);
      entries.set(key, entry);
      const matched = matchedResponse(entry, metadata.requestHeaders, now);
      const details = encoder.encode(
        JSON.stringify({
          status: matched.status,
          statusText: matched.statusText,
          headers: [...matched.headers],
        }),
      );
      return new Response(matched.body, {
        headers: { "x-krun-cache-response": btoa(String.fromCharCode(...details)) },
      });
    }
    return miss();
  }
  if (
    request.method !== "PUT" ||
    !Array.isArray(metadata.headers) ||
    metadata.headers.length > 100 ||
    !Number.isInteger(metadata.status) ||
    metadata.status < 200 ||
    metadata.status > 599 ||
    metadata.status === 206 ||
    typeof metadata.statusText !== "string" ||
    metadata.statusText.length > 128
  ) {
    request.body?.cancel().catch(() => {});
    return new Response(null, { status: 400 });
  }
  const headers = new Headers(metadata.headers);
  const duration = ttl(headers, metadata.status, now);
  const varyNames = [
    ...new Set(
      (headers.get("vary") || "")
        .toLowerCase()
        .split(",")
        .map((value) => value.trim())
        .filter(Boolean),
    ),
  ];
  const length = Number(request.headers.get("content-length"));
  if (
    !duration ||
    varyNames.includes("*") ||
    varyNames.length > 32 ||
    activeWrites >= 2 ||
    length > maxEntryBytes
  ) {
    request.body?.cancel().catch(() => {});
    return new Response(null, { status: 204 });
  }
  activeWrites++;
  const reader = request.body?.getReader();
  let size = 0;
  const chunks = [];
  try {
    if (reader) {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) {
          break;
        }
        size += value.byteLength;
        if (size > maxEntryBytes) {
          reader.cancel().catch(() => {});
          return new Response(null, { status: 204 });
        }
        chunks.push(value);
      }
    }
    const bytes = new Uint8Array(size);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.byteLength;
    }
    const vary = varyNames.map((name) => [name, metadata.requestHeaders.get(name)]);
    const key = JSON.stringify([base, vary]);
    const entry = {
      base,
      vary,
      bytes,
      headers: [...headers],
      status: metadata.status,
      statusText: metadata.statusText,
      created: now,
      expires: now + duration,
      age: Math.max(0, Number(headers.get("age")) || 0),
      weight:
        size + encoder.encode(JSON.stringify(metadata)).byteLength + encoder.encode(key).byteLength,
    };
    drop(key);
    for (const [oldKey, old] of entries) {
      if (old.expires <= now) {
        drop(oldKey);
      }
    }
    entries.set(key, entry);
    if (!index.has(base)) {
      index.set(base, new Set());
    }
    index.get(base).add(key);
    storedBytes += entry.weight;
    while (storedBytes > maxBytes || entries.size > maxEntries) {
      drop(entries.keys().next().value);
    }
    return new Response(null, { status: 204 });
  } finally {
    activeWrites--;
    reader?.releaseLock();
  }
}

export function createCacheStorage(capability) {
  function cache(name) {
    function key(input, options = {}) {
      if (
        !options ||
        typeof options !== "object" ||
        Object.keys(options).some((field) => field !== "ignoreMethod") ||
        (options.ignoreMethod !== undefined && typeof options.ignoreMethod !== "boolean")
      ) {
        throw new TypeError("Unsupported cache option");
      }
      const request = input instanceof Request ? input : new Request(input);
      const url = new URL(request.url);
      url.hash = "";
      if (!["http:", "https:"].includes(url.protocol)) {
        throw new TypeError("Cache keys must use HTTP or HTTPS");
      }
      return {
        request,
        metadata: { name, url: url.href, requestHeaders: [...request.headers] },
        ignoreMethod: options.ignoreMethod,
      };
    }
    async function send(method, metadata, body) {
      const bytes = encoder.encode(JSON.stringify(metadata));
      if (bytes.byteLength > 24000) {
        throw new TypeError("Cache metadata exceeds the runtime limit");
      }
      return capability.fetch("http://cache.local/", {
        method,
        headers: { "x-krun-cache": btoa(String.fromCharCode(...bytes)) },
        body,
        signal: AbortSignal.timeout(30000),
      });
    }
    return Object.freeze({
      async match(input, options) {
        const { request, metadata, ignoreMethod } = key(input, options);
        if (request.method !== "GET" && !ignoreMethod) {
          return undefined;
        }
        const response = await send("GET", metadata);
        if (response.status === 504) {
          await response.body?.cancel();
          return undefined;
        }
        if (response.status === 400) {
          await response.body?.cancel();
          throw new TypeError("Invalid cache request");
        }
        if (!response.ok) {
          await response.body?.cancel();
          throw new Error("Cache read failed");
        }
        const details = JSON.parse(
          new TextDecoder().decode(
            Uint8Array.from(atob(response.headers.get("x-krun-cache-response")), (character) =>
              character.charCodeAt(0),
            ),
          ),
        );
        return new Response([204, 205, 304].includes(details.status) ? null : response.body, {
          ...details,
          encodeBody: "manual",
        });
      },
      async put(input, response) {
        const { request, metadata } = key(input);
        if (
          request.method !== "GET" ||
          !(response instanceof Response) ||
          response.status === 206 ||
          (response.headers.get("vary") || "").split(",").some((value) => value.trim() === "*")
        ) {
          throw new TypeError("Response or method cannot be cached");
        }
        if (response.bodyUsed) {
          throw new TypeError("Response body has already been consumed");
        }
        const result = await send(
          "PUT",
          {
            ...metadata,
            status: response.status,
            statusText: response.statusText,
            headers: [...response.headers],
          },
          response.body,
        );
        await result.body?.cancel();
        if (!result.ok) {
          throw new Error("Cache write failed");
        }
      },
      async delete(input, options) {
        const { request, metadata, ignoreMethod } = key(input, options);
        if (request.method !== "GET" && !ignoreMethod) {
          return false;
        }
        const response = await send("DELETE", metadata);
        await response.body?.cancel();
        if (response.status === 404) {
          return false;
        }
        if (!response.ok) {
          throw new Error("Cache delete failed");
        }
        return true;
      },
    });
  }
  const defaultCache = cache("");
  return Object.freeze({
    default: defaultCache,
    async open(name) {
      if (typeof name !== "string" || name.length > 1024) {
        throw new TypeError("Invalid cache name");
      }
      return cache(":" + name);
    },
  });
}
