import { objectMetadata, httpFields } from "./metadata.js";
import { smallXML, xmlBlocks, xmlTag } from "./xml.js";

// Fetcher bindings work before the RPC compatibility date as well. Only
// bounded signing metadata crosses this local control call, never object bytes.
export function createStorageSigner(capability) {
  async function call(action, args) {
    const response = await capability.fetch("http://storage-signer/" + action, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(args),
      signal: AbortSignal.timeout(30000),
    });
    const result = await response.json();
    if (!response.ok) {
      if (result.type === "TypeError") {
        throw new TypeError(result.error);
      }
      throw new Error(result.error || "Storage signer unavailable");
    }
    return result;
  }
  return {
    sign: (operation, key, options) => call("sign", { operation, key, options }),
    cursor: (token, options) => call("cursor", { token, options }),
    multipartToken: (key, uploadId, version) => call("multipart-token", { key, uploadId, version }),
  };
}

export function createR2Bucket(signer, transport, profile = {}) {
  function normalize(options) {
    const out = { ...options };
    if (out.httpMetadata instanceof Headers) {
      out.httpMetadata = Object.fromEntries(
        Object.entries(httpFields)
          .filter(([, header]) => out.httpMetadata.has(header))
          .map(([name, header]) => [name, out.httpMetadata.get(header)]),
      );
    }
    if (out.onlyIf instanceof Headers) {
      const mapping = {
        "if-match": "etagMatches",
        "if-none-match": "etagDoesNotMatch",
        "if-modified-since": "uploadedAfter",
        "if-unmodified-since": "uploadedBefore",
      };
      out.onlyIf = Object.fromEntries(
        Object.entries(mapping)
          .filter(([header]) => out.onlyIf.has(header))
          .map(([header, name]) => [name, out.onlyIf.get(header)]),
      );
      out.onlyIf.secondsGranularity = true;
    }
    if (out.range instanceof Headers) {
      const value = out.range.get("range");
      const match = value?.match(/^bytes=(\d*)-(\d*)$/);
      if (!match || (!match[1] && !match[2])) {
        throw new TypeError("Invalid range header");
      }
      out.range = !match[1]
        ? { suffix: Number(match[2]) }
        : match[2]
          ? { offset: Number(match[1]), length: Number(match[2]) - Number(match[1]) + 1 }
          : { offset: Number(match[1]) };
    }
    return out;
  }
  function matches(object, condition = {}) {
    const etag = (value) => value === "*" || value.replace(/^"|"$/g, "") === object.etag;
    if (condition.etagMatches !== undefined && !etag(condition.etagMatches)) {
      return false;
    }
    if (condition.etagDoesNotMatch !== undefined && etag(condition.etagDoesNotMatch)) {
      return false;
    }
    if (
      condition.uploadedBefore !== undefined &&
      object.uploaded > new Date(condition.uploadedBefore)
    ) {
      return false;
    }
    if (
      condition.uploadedAfter !== undefined &&
      object.uploaded <= new Date(condition.uploadedAfter)
    ) {
      return false;
    }
    return true;
  }
  const send = async (operation, key, options = {}, body) => {
    const signed = await signer.sign(operation, key, normalize(options));
    const controller = new AbortController();
    return {
      signed,
      controller,
      response: await transport.fetch(signed.url, {
        method: signed.method,
        headers: signed.headers,
        body,
        redirect: "manual",
        signal: AbortSignal.any([
          controller.signal,
          AbortSignal.timeout(signed.timeoutMs ?? 600000),
        ]),
      }),
    };
  };
  const head = async (key, options = {}) => {
    const { response } = await send("head", key, options);
    if (response.status === 404) {
      await response.body?.cancel();
      return null;
    }
    if (!response.ok) {
      await response.body?.cancel();
      throw new Error("R2 HEAD failed (" + response.status + ")");
    }
    return objectMetadata(key, response);
  };
  const etag = (value) => value.replace(/^"|"$/g, "");
  const escapeXML = (value) =>
    value.replace(
      /[<>&"']/g,
      (character) =>
        ({
          "<": "&lt;",
          ">": "&gt;",
          "&": "&amp;",
          '"': "&quot;",
          "'": "&apos;",
        })[character],
    );

  async function multipartXML(operation, key, options, body) {
    const result = await send(operation, key, options, body);
    const xml = await smallXML(result.response);
    const code = xmlTag(xml, "Code");
    if (!result.response.ok || code) {
      const safe = /^[A-Za-z0-9]{1,64}$/.test(code) ? code : String(result.response.status);
      throw new Error("R2 " + operation + " failed (" + safe + ")");
    }
    return { ...result, xml };
  }

  function multipart(key, uploadId) {
    if (
      typeof key !== "string" ||
      !key ||
      typeof uploadId !== "string" ||
      !uploadId ||
      uploadId.length > 8192
    ) {
      throw new TypeError("Invalid multipart identity");
    }
    return Object.freeze({
      key,
      uploadId,
      async uploadPart(partNumber, value, options = {}) {
        if (!options || typeof options !== "object" || Object.keys(options).length) {
          throw new TypeError("Unsupported multipart part options");
        }
        if (
          typeof value !== "string" &&
          !(value instanceof ArrayBuffer) &&
          !ArrayBuffer.isView(value) &&
          !(value instanceof Blob) &&
          !(value instanceof ReadableStream)
        ) {
          throw new TypeError("Invalid multipart part value");
        }
        const { response } = await send("multipart-part", key, { uploadId, partNumber }, value);
        await response.body?.cancel();
        const tag = response.headers.get("etag");
        if (!response.ok || !tag) {
          throw new Error("R2 multipart part upload failed (" + response.status + ")");
        }
        return { partNumber, etag: etag(tag) };
      },
      async abort() {
        const { response } = await send("multipart-abort", key, { uploadId });
        await response.body?.cancel();
        if (!response.ok && response.status !== 404) {
          throw new Error("R2 multipart abort failed (" + response.status + ")");
        }
      },
      async complete(parts) {
        if (!Array.isArray(parts) || !parts.length || parts.length > 10000) {
          throw new TypeError("Invalid multipart parts");
        }
        let previous = 0;
        const selected = parts.map((part) => {
          if (
            !part ||
            !Number.isInteger(part.partNumber) ||
            part.partNumber <= previous ||
            part.partNumber > 10000 ||
            typeof part.etag !== "string" ||
            !part.etag ||
            part.etag.length > 1024 ||
            /[\r\n]/.test(part.etag)
          ) {
            throw new TypeError("Invalid or unordered multipart parts");
          }
          previous = part.partNumber;
          return { partNumber: part.partNumber, etag: etag(part.etag) };
        });
        // Inspect backend sizes rather than retaining part state in an isolate.
        // This enforces R2's equal-sized non-final parts across resumed requests.
        const uploaded = new Map();
        let marker = 0;
        for (let page = 0; page < 11; page++) {
          const { xml } = await multipartXML("multipart-list", key, { uploadId, marker });
          if (!/<ListPartsResult(?:\s|>)/.test(xml)) {
            throw new Error("Invalid multipart listing response");
          }
          for (const block of xmlBlocks(xml, "Part")) {
            const number = Number(xmlTag(block, "PartNumber"));
            const size = Number(xmlTag(block, "Size"));
            if (
              !Number.isInteger(number) ||
              number <= marker ||
              number > 10000 ||
              uploaded.has(number) ||
              !Number.isSafeInteger(size) ||
              size < 0 ||
              size > 5 * 1024 ** 3 ||
              uploaded.size >= 10000
            ) {
              throw new Error("Invalid multipart part metadata");
            }
            uploaded.set(number, { size, etag: etag(xmlTag(block, "ETag")) });
          }
          if (xmlTag(xml, "IsTruncated") !== "true") {
            break;
          }
          const next = Number(xmlTag(xml, "NextPartNumberMarker"));
          if (!Number.isInteger(next) || next <= marker || next > 10000 || page === 10) {
            throw new Error("Invalid multipart listing continuation");
          }
          marker = next;
        }
        let standardSize;
        for (let index = 0; index < selected.length; index++) {
          const part = selected[index];
          const actual = uploaded.get(part.partNumber);
          if (!actual || actual.etag !== part.etag) {
            throw new Error("Multipart part is missing or its ETag changed");
          }
          if (index < selected.length - 1) {
            standardSize ??= actual.size;
            if (actual.size < 5 * 1024 ** 2 || actual.size !== standardSize) {
              throw new Error(
                "Multipart non-final parts must have the same size and be at least 5 MiB",
              );
            }
          } else if (standardSize !== undefined && actual.size > standardSize) {
            throw new Error("Multipart final part exceeds the other parts");
          }
        }
        const body =
          "<CompleteMultipartUpload>" +
          selected
            .map(
              (part) =>
                "<Part><PartNumber>" +
                part.partNumber +
                "</PartNumber><ETag>" +
                escapeXML('"' + part.etag + '"') +
                "</ETag></Part>",
            )
            .join("") +
          "</CompleteMultipartUpload>";
        const { signed, xml } = await multipartXML("multipart-complete", key, { uploadId }, body);
        if (!/<CompleteMultipartUploadResult(?:\s|>)/.test(xml)) {
          throw new Error("Invalid multipart completion response");
        }
        const object = await head(key);
        if (
          !object ||
          object.version !== signed.version ||
          object.etag !== etag(xmlTag(xml, "ETag"))
        ) {
          throw new Error("R2 multipart completion could not be confirmed");
        }
        return object;
      },
    });
  }
  return Object.freeze({
    head,
    async get(key, options = {}) {
      options = normalize(options);
      for (let attempt = 0; attempt < 2; attempt++) {
        const { response, controller } = await send("get", key, options);
        if (response.status === 404) {
          await response.body?.cancel();
          return null;
        }
        if (response.status === 304 || response.status === 412) {
          await response.body?.cancel();
          const object = await head(key);
          if (object && !matches(object, options.onlyIf)) {
            return object;
          }
          continue;
        }
        if (!response.ok) {
          await response.body?.cancel();
          throw new Error("R2 GET failed (" + response.status + ")");
        }
        try {
          return objectMetadata(key, response, true, () => controller.abort());
        } catch (error) {
          await response.body?.cancel();
          throw error;
        }
      }
      throw new Error("Conditional result changed during metadata retrieval");
    },
    async put(key, value, options = {}) {
      if (
        value !== null &&
        typeof value !== "string" &&
        !(value instanceof ArrayBuffer) &&
        !ArrayBuffer.isView(value) &&
        !(value instanceof Blob) &&
        !(value instanceof ReadableStream)
      ) {
        throw new TypeError("Invalid R2 value");
      }
      const signed = await signer.sign("put", key, normalize(options));
      const response = await transport.fetch(signed.url, {
        method: signed.method,
        headers: signed.headers,
        body: value,
        redirect: "manual",
        signal: AbortSignal.timeout(signed.timeoutMs ?? 600000),
      });
      await response.body?.cancel();
      if (response.status === 412) {
        return null;
      }
      if (!response.ok) {
        throw new Error("R2 PUT failed (" + response.status + ")");
      }
      const object = await head(key);
      if (!object || object.version !== signed.version) {
        throw new Error("R2 write result could not be confirmed");
      }
      return object;
    },
    async delete(keys) {
      const list = Array.isArray(keys) ? keys : [keys];
      if (list.length > 1000) {
        throw new TypeError("At most 1000 keys may be deleted");
      }
      // Bounded batches preserve per-key errors instead of accepting an S3
      // HTTP 200 that can contain partial DeleteObjects failures.
      for (let offset = 0; offset < list.length; offset += 8) {
        await Promise.all(
          list.slice(offset, offset + 8).map(async (key) => {
            const { response } = await send("delete", key);
            await response.body?.cancel();
            if (!response.ok && response.status !== 404) {
              throw new Error("R2 DELETE failed (" + response.status + ")");
            }
          }),
        );
      }
    },
    async list(options = {}) {
      if (profile.legacyListInclude) {
        options = { ...options, include: ["httpMetadata", "customMetadata"] };
      }
      const { signed, response } = await send("list", "", options);
      if (!response.ok) {
        await response.body?.cancel();
        throw new Error("R2 LIST failed (" + response.status + ")");
      }
      const xml = await smallXML(response);
      const keys = xmlBlocks(xml, "Contents").map((block) => xmlTag(block, "Key"));
      const objects = [];
      for (let offset = 0; offset < keys.length; offset += 8) {
        const batch = await Promise.all(
          keys.slice(offset, offset + 8).map(async (full) => {
            if (!full.startsWith(signed.prefix)) {
              throw new Error("Storage returned an object outside the binding");
            }
            const object = await head(full.slice(signed.prefix.length));
            if (!object) {
              return null;
            }
            if (!options.include?.includes("httpMetadata")) {
              delete object.httpMetadata;
            }
            if (!options.include?.includes("customMetadata")) {
              delete object.customMetadata;
            }
            return object;
          }),
        );
        objects.push(...batch.filter(Boolean));
      }
      const delimitedPrefixes = xmlBlocks(xml, "CommonPrefixes")
        .map((block) => xmlTag(block, "Prefix"))
        .map((prefix) => {
          if (!prefix.startsWith(signed.prefix)) {
            throw new Error("Storage returned an invalid prefix");
          }
          return prefix.slice(signed.prefix.length);
        });
      const truncated = xmlTag(xml, "IsTruncated") === "true";
      const cursor = truncated
        ? await signer.cursor(xmlTag(xml, "NextContinuationToken"), options)
        : undefined;
      if (truncated && !cursor) {
        throw new Error("Storage returned an invalid continuation token");
      }
      return { objects, truncated, cursor, delimitedPrefixes };
    },
    async createMultipartUpload(key, options = {}) {
      const { signed, xml } = await multipartXML("multipart-create", key, normalize(options));
      const uploadId = xmlTag(xml, "UploadId");
      if (!/<InitiateMultipartUploadResult(?:\s|>)/.test(xml) || !uploadId) {
        throw new Error("Invalid multipart creation response");
      }
      const token = await signer.multipartToken(key, uploadId, signed.version);
      return multipart(key, token);
    },
    resumeMultipartUpload(key, uploadId) {
      return multipart(key, uploadId);
    },
  });
}
