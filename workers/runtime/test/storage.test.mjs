import assert from "node:assert/strict";
import { createHash, createHmac } from "node:crypto";
import { configurationRequest } from "../storage.js";
const env = {
  CONFIG_BASE: "https://bucket.storage.example.com/dev/run/config/pools/default",
  STORAGE_ACCESS_KEY: "test-access",
  STORAGE_SECRET_KEY: "test-secret",
  STORAGE_REGION: "cn-beijing",
};
const now = new Date("2026-11-03T01:02:03Z");
const request = await configurationRequest(
  env,
  env.CONFIG_BASE + "/shards/ab/latest.json",
  1000,
  now,
);
const sha = (value) => createHash("sha256").update(value).digest("hex");
const hmac = (key, value) => createHmac("sha256", key).update(value).digest();
const stamp = "20261103T010203Z";
const scope = "20261103/cn-beijing/s3/aws4_request";
const canonical = [
  "GET",
  "/dev/run/config/pools/default/shards/ab/latest.json",
  "",
  "host:bucket.storage.example.com\nx-amz-content-sha256:" +
    sha("") +
    "\nx-amz-date:" +
    stamp +
    "\n",
  "host;x-amz-content-sha256;x-amz-date",
  sha(""),
].join("\n");
let key = "AWS4test-secret";
for (const value of ["20261103", "cn-beijing", "s3", "aws4_request"]) {
  key = hmac(key, value);
}
const signature = hmac(
  key,
  "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + sha(canonical),
).toString("hex");
assert.equal(
  request.headers.get("authorization"),
  "AWS4-HMAC-SHA256 Credential=test-access/" +
    scope +
    ", SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=" +
    signature,
);
assert.equal(request.redirect, "manual");
for (const path of [
  "https://other.example.com/dev/run/config/pools/default/head",
  "https://bucket.storage.example.com/another-app/head",
  env.CONFIG_BASE + "/../private",
  env.CONFIG_BASE + "/head?leak=1",
]) {
  await assert.rejects(configurationRequest(env, path, 1000));
}
assert.equal(
  (
    await configurationRequest(
      { CONFIG_BASE: env.CONFIG_BASE },
      env.CONFIG_BASE + "/latest.json",
      1000,
    )
  ).headers.get("authorization"),
  null,
);
await assert.rejects(
  configurationRequest({ ...env, STORAGE_SECRET_KEY: "" }, env.CONFIG_BASE + "/latest.json", 1000),
);
console.log(
  "Private config signing matches independent Node crypto; origin/prefix guards and anonymous managed storage passed",
);
