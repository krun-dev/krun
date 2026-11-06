import test from "node:test";
import assert from "node:assert/strict";
import { readBuildLog } from "./build-logs.ts";

test("browser reads inline logs through one same-origin API request", async (t) => {
  const path = "/api/v1/orgs/org/workers/worker/builds/build/logs";
  const calls = [];
  t.mock.method(globalThis, "fetch", async (url, options) => {
    calls.push(url);
    assert.equal(url, path + "?inline=true");
    assert.equal(options.credentials, "same-origin");
    return Response.json(
      {
        state: "building",
        log: "compiling\n",
        download_url: "https://object.test/log.txt",
      },
      { headers: { "X-Log-Poll-Interval": "10" } },
    );
  });
  assert.deepEqual(await readBuildLog(path), {
    state: "building",
    log: "compiling\n",
    pollSeconds: 10,
  });
  assert.equal(calls.length, 1);
});

test("final empty logs stop polling and preserve existing query parameters", async (t) => {
  t.mock.method(globalThis, "fetch", async (url) => {
    assert.equal(url, "/api/v1/logs?existing=value&inline=true");
    return Response.json({ state: "succeeded", log: "" });
  });
  assert.deepEqual(await readBuildLog("/api/v1/logs?existing=value"), {
    state: "succeeded",
    log: "",
    pollSeconds: null,
  });
});

test("inline storage errors surface without a cross-origin fallback", async (t) => {
  const fetch = t.mock.method(globalThis, "fetch", async () =>
    Response.json(
      { error: { code: "LOG_UNAVAILABLE", message: "Log storage unavailable" } },
      { status: 503 },
    ),
  );
  await assert.rejects(readBuildLog("/api/v1/logs"), {
    code: "LOG_UNAVAILABLE",
    status: 503,
  });
  assert.equal(fetch.mock.callCount(), 1);
});
