import test from "node:test";
import assert from "node:assert/strict";
import worker from "../src/index.js";

const env = { KRUN_ENVIRONMENT: "test", KRUN_RELEASE_ID: "rel_test" };

test("health exposes release context without caching", async () => {
  const response = await worker.fetch(new Request("https://example.test/api/health"), env);
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("cache-control"), "no-store");
  assert.deepEqual(await response.json(), {
    ok: true,
    service: "krun-workers-fullstack",
    environment: "test",
    release_id: "rel_test",
  });
});

test("echo validates methods, JSON, and payloads", async () => {
  const good = await worker.fetch(
    new Request("https://example.test/api/echo", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ message: "round trip" }),
    }),
    env,
  );
  assert.equal(good.status, 200);
  assert.equal((await good.json()).echo, "round trip");

  const method = await worker.fetch(new Request("https://example.test/api/echo"), env);
  assert.equal(method.status, 405);
  assert.equal(method.headers.get("allow"), "POST");

  const invalid = await worker.fetch(
    new Request("https://example.test/api/echo", { method: "POST", body: "{" }),
    env,
  );
  assert.equal(invalid.status, 400);
});

test("unknown paths are explicit JSON 404s", async () => {
  const response = await worker.fetch(new Request("https://example.test/api/missing"), env);
  assert.equal(response.status, 404);
  assert.deepEqual(await response.json(), { error: "not_found", path: "/api/missing" });
});
