import assert from "node:assert/strict";
import { prepareStorageRequest, createMultipartToken } from "../storage/signer.js";
import { createR2Bucket } from "../storage/binding.js";

const grant = {
  endpoint: "https://objects.example.com",
  region: "cn-beijing",
  bucket: "physical",
  prefix: "root/org/a/buckets/b/",
  bucket_id: "logical",
  store_id: "primary",
  addressing_style: "path",
  access_key_id: "fixture-access",
  secret_access_key: "fixture-secret",
  cursor_key: "fixture-signing-key",
};
const version = "00000000-0000-4000-8000-000000000000";
const token = await createMultipartToken(grant, "file.txt", "backend+/=id", version);
const part = await prepareStorageRequest(grant, "multipart-part", "file.txt", {
  uploadId: token,
  partNumber: 3,
});
const url = new URL(part.url);
assert.equal(url.searchParams.get("uploadId"), "backend+/=id");
assert.equal(url.searchParams.get("partNumber"), "3");
assert.equal(url.pathname, "/physical/" + grant.prefix + "file.txt");
for (const [key, options] of [
  ["different.txt", { uploadId: token, partNumber: 1 }],
  ["file.txt", { uploadId: token + "x", partNumber: 1 }],
  ["file.txt", { uploadId: token, partNumber: 0 }],
  ["file.txt", { uploadId: token, partNumber: 10001 }],
]) {
  await assert.rejects(prepareStorageRequest(grant, "multipart-part", key, options), TypeError);
}
await assert.rejects(
  prepareStorageRequest({ ...grant, bucket_id: "different" }, "multipart-abort", "file.txt", {
    uploadId: token,
  }),
  TypeError,
);
const now = Date.now;
try {
  Date.now = () => now() + 8 * 86400000;
  await assert.rejects(
    prepareStorageRequest(grant, "multipart-abort", "file.txt", { uploadId: token }),
    TypeError,
  );
} finally {
  Date.now = now;
}

let storedMetadata;
let completeCalls = 0;
let embeddedError = false;
let secondSize = 4;
let wholeVersion;
const signer = {
  sign: (operation, key, options) => prepareStorageRequest(grant, operation, key, options),
  multipartToken: (key, id, identity) => createMultipartToken(grant, key, id, identity),
};
const bucket = createR2Bucket(signer, {
  async fetch(input, init) {
    const target = new URL(input);
    if (target.searchParams.has("uploads")) {
      storedMetadata = init.headers["x-amz-meta-krun-v1"];
      wholeVersion = JSON.parse(Buffer.from(storedMetadata, "base64")).version;
      return new Response(
        "<InitiateMultipartUploadResult><UploadId>backend-id</UploadId></InitiateMultipartUploadResult>",
      );
    }
    if (init.method === "GET" && target.searchParams.has("uploadId")) {
      return new Response(
        "<ListPartsResult><IsTruncated>false</IsTruncated>" +
          '<Part><PartNumber>1</PartNumber><Size>5242880</Size><ETag>"first"</ETag></Part>' +
          "<Part><PartNumber>2</PartNumber><Size>" +
          secondSize +
          '</Size><ETag>"second"</ETag></Part></ListPartsResult>',
      );
    }
    if (init.method === "POST") {
      completeCalls++;
      assert.match(init.body, /&quot;first&quot;/);
      return new Response(
        embeddedError
          ? "<Error><Code>InvalidPart</Code></Error>"
          : '<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>',
      );
    }
    if (init.method === "HEAD") {
      return new Response(null, {
        headers: {
          "x-amz-meta-krun-v1": storedMetadata,
          "content-length": "5242884",
          etag: '"complete"',
          "last-modified": "Wed, 04 Nov 2026 00:00:00 GMT",
        },
      });
    }
    if (init.method === "DELETE") {
      return new Response(null, { status: 404 });
    }
    return new Response(null, { headers: { etag: '"first"' } });
  },
});
const upload = await bucket.createMultipartUpload("file.txt", {
  customMetadata: { original: "中文" },
});
const resumed = bucket.resumeMultipartUpload(upload.key, upload.uploadId);
assert.equal(typeof resumed.then, "undefined", "resume must be synchronous");
assert.deepEqual(await resumed.uploadPart(1, "fixture"), { partNumber: 1, etag: "first" });
const parts = [
  { partNumber: 1, etag: "first" },
  { partNumber: 2, etag: "second" },
];
await assert.rejects(resumed.complete([...parts].reverse()), TypeError);
await assert.rejects(resumed.complete([{ partNumber: 1, etag: "changed" }]), /ETag changed/);
assert.equal(completeCalls, 0);
embeddedError = true;
await assert.rejects(resumed.complete(parts), /InvalidPart/);
embeddedError = false;
secondSize = 6 * 1024 * 1024;
await assert.rejects(resumed.complete(parts), /final part exceeds/);
secondSize = 4;
const object = await resumed.complete(parts);
assert.equal(object.version, wholeVersion);
assert.deepEqual(object.customMetadata, { original: "中文" });
await resumed.abort();
await resumed.abort();
console.log(
  "Multipart scope, expiry, resume, ETags, sizes, HTTP 200 errors and completion metadata passed",
);
