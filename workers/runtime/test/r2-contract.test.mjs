import assert from "node:assert/strict";
import { prepareStorageRequest, createCursor } from "../storage/signer.js";
import { encodeMetadata, objectMetadata } from "../storage/metadata.js";
import { createR2Bucket } from "../storage/binding.js";

const grant = {
  endpoint: "https://storage.example.com",
  addressing_style: "virtual",
  bucket: "physical-bucket",
  bucket_id: "logical-bucket",
  region: "us-east-1",
  prefix: "worker-data/org/org-a/buckets/bucket-a/",
  access_key_id: "example-access",
  secret_access_key: "secret-never-serialized",
  session_token: "example-session",
  cursor_key: "example-cursor-secret",
};
const signed = await prepareStorageRequest(grant, "get", "目录/a b+%.txt");
const url = new URL(signed.url);
assert.equal(url.hostname, "physical-bucket.storage.example.com");
assert.ok(url.pathname.startsWith("/" + grant.prefix));
assert.ok(url.pathname.endsWith("/a%20b%2B%25.txt"));
assert.equal(url.searchParams.get("X-Amz-Expires"), "60");
assert.equal(url.searchParams.get("response-content-encoding"), "identity");
assert.ok(!JSON.stringify(signed).includes(grant.secret_access_key));
for (const key of ["../escape", "a/../../escape", "a\\escape", "", "a".repeat(1025)]) {
  await assert.rejects(prepareStorageRequest(grant, "get", key), TypeError);
}
const disguised = await prepareStorageRequest(grant, "get", "https://other.example/key");
assert.equal(new URL(disguised.url).hostname, url.hostname);
await assert.rejects(
  prepareStorageRequest(grant, "get", "key", { endpoint: "https://other.example" }),
  TypeError,
);
await assert.rejects(
  prepareStorageRequest(grant, "put", "key", { sha256: "ignored-checksum" }),
  TypeError,
);
await assert.rejects(
  prepareStorageRequest(grant, "put", "key", { onlyIf: { etagDoesNotMatch: "arbitrary" } }),
  TypeError,
);
await assert.rejects(
  prepareStorageRequest(grant, "get", "key", {
    onlyIf: { etagMatches: "a", etagDoesNotMatch: "b" },
  }),
  TypeError,
);
const conditional = await prepareStorageRequest(grant, "put", "key", {
  onlyIf: { etagDoesNotMatch: "*" },
});
assert.equal(conditional.headers["if-none-match"], "*");
assert.ok(conditional.version);

const options = { prefix: "folder/", delimiter: "/", include: ["customMetadata"] };
const flatList = await prepareStorageRequest(grant, "list", "", { delimiter: "" });
assert.equal(new URL(flatList.url).searchParams.has("delimiter"), false);
await assert.rejects(prepareStorageRequest(grant, "list", "", { delimiter: ":" }), TypeError);
const cursor = await createCursor(grant, "opaque+/=upstream-token", options);
const page = await prepareStorageRequest(grant, "list", "", { ...options, cursor });
const pageURL = new URL(page.url);
assert.equal(pageURL.searchParams.get("continuation-token"), "opaque+/=upstream-token");
assert.equal(pageURL.searchParams.get("prefix"), grant.prefix + options.prefix);
const tampered = cursor.slice(0, -1) + (cursor.endsWith("X") ? "Y" : "X");
await assert.rejects(
  prepareStorageRequest(grant, "list", "", { ...options, cursor: tampered }),
  TypeError,
);
await assert.rejects(
  prepareStorageRequest({ ...grant, bucket_id: "another-bucket" }, "list", "", {
    ...options,
    cursor,
  }),
  TypeError,
);
await assert.rejects(
  prepareStorageRequest(grant, "list", "", { ...options, prefix: "other/", cursor }),
  TypeError,
);

const expiry = new Date("2030-01-01T00:00:00.123Z");
const metadata = encodeMetadata("version-test", {
  customMetadata: { MixedCase: "保留" },
  httpMetadata: { cacheExpiry: expiry, contentType: "text/plain" },
});
const headers = {
  ...metadata,
  "content-length": "0",
  "last-modified": "Wed, 04 Nov 2026 01:00:00 GMT",
  etag: '"opaque-etag"',
};
const object = objectMetadata("empty", new Response(new Uint8Array(), { headers }), true);
assert.equal(object.size, 0);
assert.equal(object.version, "version-test");
assert.equal(object.httpMetadata.cacheExpiry.valueOf(), expiry.valueOf());
assert.deepEqual(object.customMetadata, { MixedCase: "保留" });
assert.deepEqual(object.checksums.toJSON(), {});
assert.equal(object.bodyUsed, false);
assert.equal((await object.arrayBuffer()).byteLength, 0);
assert.equal(object.bodyUsed, true);
const outputHeaders = new Headers();
object.writeHttpMetadata(outputHeaders);
assert.equal(outputHeaders.get("expires"), expiry.toUTCString());
for (const missing of ["last-modified", "content-length", "x-amz-meta-krun-v1"]) {
  const invalid = new Headers(headers);
  invalid.delete(missing);
  assert.throws(() => objectMetadata("bad", new Response(null, { headers: invalid })), /metadata/);
}
assert.throws(
  () => encodeMetadata("version", { customMetadata: { huge: "x".repeat(4096) } }),
  TypeError,
);
const forbidden = createR2Bucket(
  { sign: () => prepareStorageRequest(grant, "get", "key") },
  { fetch: () => new Response("AccessDenied", { status: 403 }) },
);
await assert.rejects(forbidden.get("key"), /403/);
console.log(
  "R2 scope, metadata, cursor authentication, conditions and authorization errors passed",
);
