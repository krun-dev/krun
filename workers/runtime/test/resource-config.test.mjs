import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { loadRuntimeSource, fixtureConfigEpoch } from "./entry-source.mjs";

const directory = mkdtempSync(join(tmpdir(), "krun-resource-config-"));
try {
  writeFileSync(
    join(directory, "entry.mjs"),
    loadRuntimeSource() + "\nexport { applyAgentHead, resolveAgentRoute, configMetricSamples };\n",
  );
  const entry = await import("file://" + join(directory, "entry.mjs"));
  const handle = "b".repeat(64);
  const head = {
    config_epoch: fixtureConfigEpoch,
    config_revision: 0,
    lazy_routes: true,
    config_sync_healthy: true,
    config_fresh: true,
    config_current: true,
    confirmed_unix_millis: Date.now(),
  };
  const route = {
    host: "site.test",
    worker_id: "site",
    environment: "production",
    version: "fixed-version",
    enabled: true,
    fixed: true,
  };
  const bundle = {
    handle,
    config_epoch: fixtureConfigEpoch,
    route,
    target_generation: 42,
    versions: {
      "fixed-version": {
        id: "fixed-version",
        worker_id: "site",
        environment: "production",
        organization_id: "team",
      },
    },
  };
  let calls = 0;
  const env = {
    CONFIG_PROTOCOL: "2",
    MANAGEMENT_TOKEN: "fixture-management",
    POOL_COORDINATOR: {
      async fetch(url, init) {
        calls++;
        assert.equal(init.headers.authorization, "Bearer fixture-management");
        assert.equal(new URL(url).pathname, "/config/bundle/" + handle);
        return Response.json(bundle);
      },
    },
  };
  entry.applyAgentHead(head);
  const request = new Request("http://site.test/", {
    headers: { "x-krun-config-handle": handle },
  });
  const first = await entry.resolveAgentRoute(env, "site.test", new URL(request.url), request);
  assert.equal(first.version, "fixed-version");
  assert.equal(first.config_generation, 42);
  for (let i = 0; i < 1000; i++) {
    assert.equal(
      await entry.resolveAgentRoute(env, "site.test", new URL(request.url), request),
      first,
    );
  }
  assert.equal(calls, 1, "hot handles must avoid additional local RPCs");
  assert.ok(entry.configMetricSamples(env).includes("krun_runtime_config_converged 1"));
  entry.applyAgentHead({ ...head, config_sync_healthy: false, config_fresh: false });
  assert.ok(entry.configMetricSamples(env).includes("krun_runtime_config_converged 0"));
  assert.equal(
    await entry.resolveAgentRoute(env, "site.test", new URL(request.url), request),
    first,
  );
  // A new epoch clears handles; the old bundle cannot silently re-enter it.
  entry.applyAgentHead({ ...head, config_epoch: "c".repeat(32) });
  env.POOL_COORDINATOR.fetch = async (url) =>
    Response.json(
      new URL(url).pathname === "/config/head" ? { ...head, config_epoch: "c".repeat(32) } : bundle,
    );
  await assert.rejects(
    entry.resolveAgentRoute(env, "site.test", new URL(request.url), request),
    /epoch changed/,
  );
  console.log(
    "Resource handles: hot RPC avoidance, fixed version, generation, outage and epoch fencing passed",
  );
} finally {
  rmSync(directory, { recursive: true, force: true });
}
