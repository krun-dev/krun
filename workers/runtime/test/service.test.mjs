import assert from "node:assert/strict";
import test from "node:test";
import { serviceCall, serviceRequest } from "../service.js";

test("a binding sends only its fixed caller identity and streams the original request", async () => {
  const payload = "first chunk then second chunk";
  const signal = new AbortController();
  const request = new Request("https://app.example/target?q=a%2Fb", {
    method: "PATCH",
    body: payload,
    headers: {
      Authorization: "Bearer app-auth",
      "Content-Type": "text/plain",
      "Krun-Workers-Version-Key": "session",
      "Cloudflare-Workers-Version-Overrides": 'app="forged"',
      "Krun-Workers-Verify-Authorization": "Bearer forged",
      "X-Krun-Service-Host": "forged.example",
      "X-Krun-Service-Version": "forged",
      "X-Runtime-Pool": "forged",
    },
    signal: signal.signal,
  });
  const response = await serviceCall(
    {
      RUNTIME_POOL: "test-pool",
      RUNTIME_TOKEN: "pool-auth",
      PLATFORM: {
        async fetch(forwarded) {
          assert.equal(
            forwarded.url,
            "http://platform/api/internal/workers/service-invoke/source-version/SERVICE",
          );
          assert.equal(forwarded.method, "PATCH");
          assert.equal(forwarded.headers.get("authorization"), "Bearer pool-auth");
          assert.equal(forwarded.headers.get("x-runtime-pool"), "test-pool");
          assert.equal(forwarded.headers.get("x-krun-service-url"), request.url);
          assert.equal(
            forwarded.headers.get("x-krun-service-app-authorization"),
            "Bearer app-auth",
          );
          assert.equal(forwarded.headers.get("content-type"), "text/plain");
          assert.equal(forwarded.headers.get("krun-workers-version-key"), "session");
          assert.equal(forwarded.headers.has("cloudflare-workers-version-overrides"), false);
          assert.equal(forwarded.headers.has("krun-workers-verify-authorization"), false);
          assert.equal(forwarded.headers.has("x-krun-service-host"), false);
          assert.equal(forwarded.headers.has("x-krun-service-version"), false);
          assert.equal(forwarded.redirect, "manual");
          assert.equal(await forwarded.text(), payload);
          signal.abort();
          assert.equal(forwarded.signal.aborted, true);
          return new Response("target response", { status: 202 });
        },
      },
    },
    { callerVersion: "source-version", binding: "SERVICE" },
    request,
  );
  assert.equal(response.status, 202);
  assert.equal(await response.text(), "target response");
});

test("the receiving runtime restores business HTTP fields and strips broker authority", async () => {
  const request = new Request("http://runtime:8083/service-invoke", {
    method: "POST",
    body: "payload",
    headers: {
      Authorization: "Bearer management",
      Host: "runtime:8083",
      "Content-Type": "text/plain",
      "Cloudflare-Workers-Version-Key": "session",
      "X-Krun-Service-Host": "target.example",
      "X-Krun-Service-Version": "fixed-version",
      "X-Krun-Service-URL": "https://app.example/path?q=1",
      "X-Krun-Service-App-Authorization": "Bearer app-auth",
    },
  });
  const business = serviceRequest(request);
  assert.equal(business.url, "https://app.example/path?q=1");
  assert.equal(business.method, "POST");
  assert.equal(business.headers.get("authorization"), "Bearer app-auth");
  assert.equal(business.headers.get("content-type"), "text/plain");
  assert.equal(business.headers.get("cloudflare-workers-version-key"), "session");
  assert.equal(business.headers.has("host"), false);
  assert.equal(business.headers.has("x-krun-service-version"), false);
  assert.equal(business.headers.has("x-krun-service-host"), false);
  assert.equal(await business.text(), "payload");
});

test("a validator does not send binding requests to the broker", async () => {
  const result = await serviceCall(
    {
      VALIDATOR_ONLY: "true",
      PLATFORM: {
        fetch() {
          assert.fail("validator contacted the service broker");
        },
      },
    },
    { callerVersion: "source-version", binding: "SERVICE" },
    new Request("https://app.example/"),
  );
  assert.equal(result.status, 403);
});
