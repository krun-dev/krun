import assert from "node:assert/strict";
import { createHash, createHmac } from "node:crypto";

export function fixtureRead(input) {
  const url = new URL(input);
  return {
    object_key: url.pathname.slice(1),
    storage: {
      endpoint: url.origin,
      region: "us-east-1",
      bucket: "runtime-fixture",
      prefix: "objects/",
      addressing_style: "path",
      lease_until: new Date(Date.now() + 300000).toISOString(),
      expires_at: new Date(Date.now() + 3600000).toISOString(),
      access_key_id: "fixture-access",
      secret_access_key: "fixture-secret",
      session_token: "fixture-session",
    },
  };
}

// Verify the real runtime signature independently, then expose the fixture's
// original path to its object server. Unsigned platform calls pass through.
export function readObjectURL(input, method = "GET") {
  const url = new URL(input);
  if (!url.pathname.startsWith("/runtime-fixture/objects/")) {
    return url;
  }
  assert.equal(url.searchParams.get("X-Amz-Security-Token"), "fixture-session");
  assert.equal(url.searchParams.get("X-Amz-Expires"), "60");
  assert.equal(url.searchParams.get("X-Amz-SignedHeaders"), "host");
  const stamp = url.searchParams.get("X-Amz-Date");
  const day = stamp.slice(0, 8);
  const scope = day + "/us-east-1/s3/aws4_request";
  assert.equal(url.searchParams.get("X-Amz-Credential"), "fixture-access/" + scope);
  const signature = url.searchParams.get("X-Amz-Signature");
  const encode = (value) =>
    encodeURIComponent(value).replace(
      /[!'()*]/g,
      (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase(),
    );
  const query = [...url.searchParams]
    .filter(([key]) => key !== "X-Amz-Signature")
    .map(([key, value]) => [encode(key), encode(value)])
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([key, value]) => key + "=" + value)
    .join("&");
  const canonical = [
    method,
    url.pathname,
    query,
    "host:" + url.host + "\n",
    "host",
    "UNSIGNED-PAYLOAD",
  ].join("\n");
  const hmac = (key, value) => createHmac("sha256", key).update(value).digest();
  let key = "AWS4fixture-secret";
  for (const part of [day, "us-east-1", "s3", "aws4_request"]) {
    key = hmac(key, part);
  }
  const expected = hmac(
    key,
    "AWS4-HMAC-SHA256\n" +
      stamp +
      "\n" +
      scope +
      "\n" +
      createHash("sha256").update(canonical).digest("hex"),
  ).toString("hex");
  assert.equal(signature, expected);
  url.pathname = url.pathname.slice("/runtime-fixture/objects".length);
  url.search = "";
  return url;
}
