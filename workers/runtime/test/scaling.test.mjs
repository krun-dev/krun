import { writeFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gzipSync } from "node:zlib";
import assert from "node:assert/strict";
import { fixtureConfigEpoch, loadRuntimeSource } from "./entry-source.mjs";
const directory = mkdtempSync(join(tmpdir(), "krun-scaling-"));
const source = loadRuntimeSource();
writeFileSync(
  join(directory, "entry.mjs"),
  source +
    `export { bootstrapFromControl, applyBatch, resolveRoute, assetManifest, recordWorkerRequest };
export const inspect = () => ({ view, versionRefs, routeVersions, manifests });
`,
);
let serial = 0;
const fresh = () => import("file://" + join(directory, "entry.mjs") + "?v=" + serial++);
const environment = (fetch) => ({
  RUNTIME_POOL: "default",
  RUNTIME_TOKEN: "fixture",
  PLATFORM: { fetch },
});
try {
  const lazy = await fresh();
  let requests = 0;
  const env = environment(async (request) => {
    const path = new URL(request.url).pathname;
    if (path.endsWith("/config/snapshot")) {
      return Response.json({
        pool: "default",
        config_epoch: fixtureConfigEpoch,
        config_revision: 1,
        lazy_routes: true,
        routes: [{ host: "cold.test", version: "stale" }],
        versions: { stale: { id: "stale" } },
      });
    }
    requests++;
    const host = decodeURIComponent(path.split("/").at(-1));
    return Response.json({
      pool: "default",
      config_epoch: fixtureConfigEpoch,
      config_revision: 2,
      routes: [{ host, version: host, enabled: true }],
      versions: { [host]: { id: host } },
    });
  });
  await lazy.bootstrapFromControl(env);
  const lookups = await Promise.all(
    Array.from({ length: 20 }, () => lazy.resolveRoute(env, "cold.test")),
  );
  assert.equal(lookups[0].version, "cold.test", "the head cannot prepopulate the route cache");
  assert.equal(requests, 1);
  // A cold lookup can observe a newer revision than the following delta feed.
  lazy.applyBatch({
    config_epoch: fixtureConfigEpoch,
    from: 1,
    to: 2,
    ops: [
      {
        type: "route_upsert",
        host: "cold.test",
        route: { host: "cold.test", version: "cold.test", enabled: true },
      },
    ],
  });
  for (let i = 0; i < 4100; i++) {
    await lazy.resolveRoute(env, `cold-${i}.test`);
  }
  const { view, routeVersions, versionRefs } = lazy.inspect();
  const { routes, versions } = view;
  assert.equal(routes.size, 4096);
  assert.equal(versions.size, 4096);
  assert.equal(routeVersions.size, 4096);
  assert.equal(versionRefs.size, 4096);
  console.log("PASS compact bootstrap, coalesced cold lookup and bounded route caches");

  // Guard the whole-index scan directly, without relying on elapsed time.
  for (const index of [routes, versions]) {
    for (const method of [Symbol.iterator, "entries", "values", "keys", "forEach"]) {
      index[method] = () => {
        throw new Error("incremental update scanned full index");
      };
    }
  }
  const cachedHost = "cold-4099.test";
  const uncachedHost = "never-visited.test";
  assert(
    lazy.applyBatch({
      config_epoch: fixtureConfigEpoch,
      from: 2,
      to: 3,
      ops: [
        { type: "version_put", version: { id: "new-version" } },
        { type: "version_put", version: { id: "uncached" } },
        {
          type: "route_upsert",
          host: cachedHost,
          route: { host: cachedHost, version: "new-version" },
        },
        {
          type: "route_upsert",
          host: uncachedHost,
          route: { host: uncachedHost, version: "uncached" },
        },
      ],
    }),
  );
  assert.equal(lazy.inspect().view.routes, routes);
  assert.equal(lazy.inspect().view.versions, versions);
  assert.equal(routes.get(cachedHost).version, "new-version");
  assert(!versions.has(cachedHost));
  assert(versions.has("new-version"));
  assert(!routes.has(uncachedHost));
  assert(!versions.has("uncached"));
  assert.throws(
    () =>
      lazy.applyBatch({
        config_epoch: fixtureConfigEpoch,
        from: 3,
        to: 4,
        ops: [{ type: "route_delete", host: cachedHost }, { type: "broken" }],
      }),
    /invalid config op broken/,
  );
  assert(routes.has(cachedHost));
  assert.equal(lazy.inspect().view.revision, 3);
  console.log(
    "PASS bounded incremental update, cold delta filtering and malformed-batch atomicity",
  );
  const assets = await fresh();
  let assetCalls = 0;
  const assetEnv = environment(async () => {
    assetCalls++;
    await new Promise((r) => setTimeout(r, 1));
    return Response.json({ config: null, files: [] });
  });
  await Promise.all(
    Array.from({ length: 20 }, () => assets.assetManifest(assetEnv, { id: "hot" })),
  );
  assert.equal(assetCalls, 1);
  const loadedVersion = { id: "loaded" };
  const loadedManifest = await assets.assetManifest(assetEnv, loadedVersion);
  assets.recordWorkerRequest(loadedVersion, {}, loadedManifest);
  for (let i = 0; i < 600; i++) {
    await assets.assetManifest(assetEnv, { id: `version-${i}` });
    await assets.assetManifest(assetEnv, { id: "hot" });
  }
  assert.equal(assets.inspect().manifests.size, 512);
  const callsBefore = assetCalls;
  await assets.assetManifest(assetEnv, { id: "hot" });
  assert.equal(assetCalls, callsBefore);
  assert.equal(assets.inspect().manifests.has("\0loaded"), false);
  assetEnv.PLATFORM.fetch = async () => new Response("unavailable", { status: 503 });
  assert.equal(await assets.assetManifest(assetEnv, loadedVersion), loadedManifest);
  await assert.rejects(assets.assetManifest(assetEnv, { id: "cold" }), /platform 503/);
  console.log("PASS single-flight manifest loading and LRU without whole-cache invalidation");
  const fallback = await fresh();
  const snapshot = {
    pool: "default",
    config_epoch: fixtureConfigEpoch,
    config_revision: 7,
    routes: [{ host: "fallback.test", version: "fallback", enabled: true }],
    versions: { fallback: { id: "fallback" } },
  };
  let controlEpoch = fixtureConfigEpoch;
  let controlRevision = 7;
  const shard = Buffer.from(
    await crypto.subtle.digest("SHA-256", new TextEncoder().encode("fallback.test")),
  )
    .toString("hex")
    .slice(0, 2);
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (input) => {
    const path = new URL(input instanceof Request ? input.url : input).pathname;
    assert(path.startsWith("/epochs/" + controlEpoch + "/"));
    const compressed = gzipSync(JSON.stringify(snapshot));
    if (path.endsWith("/latest.json")) {
      return Response.json({
        snapshot: `shards/${shard}/${snapshot.config_revision}.json.gz`,
        sha256: Buffer.from(await crypto.subtle.digest("SHA-256", compressed)).toString("hex"),
        config_epoch: snapshot.config_epoch,
        config_revision: snapshot.config_revision,
      });
    }
    return new Response(compressed);
  };
  try {
    const env = environment(async (request) => {
      if (new URL(request.url).pathname.endsWith("/config/snapshot")) {
        return Response.json({
          pool: "default",
          config_epoch: controlEpoch,
          config_revision: controlRevision,
          lazy_routes: true,
        });
      }
      throw new Error("control down");
    });
    env.CONFIG_BASE = "http://config.test";
    await fallback.bootstrapFromControl(env);
    assert.equal((await fallback.resolveRoute(env, "fallback.test")).version, "fallback");
    controlEpoch = "b".repeat(32);
    controlRevision = 3;
    await fallback.bootstrapFromControl(env);
    assert.equal(fallback.inspect().view.routes.size, 0);
    assert.equal(fallback.inspect().view.revision, 3);
    await assert.rejects(fallback.resolveRoute(env, "fallback.test"), /cursor mismatch/);
    snapshot.config_epoch = controlEpoch;
    snapshot.config_revision = 2;
    await assert.rejects(fallback.resolveRoute(env, "fallback.test"), /cursor mismatch/);
    snapshot.config_revision = 3;
    snapshot.routes[0].enabled = false;
    assert.equal((await fallback.resolveRoute(env, "fallback.test")).enabled, false);
  } finally {
    globalThis.fetch = originalFetch;
  }
  console.log("PASS shard fallback is restricted to the current epoch and revision");
} finally {
  rmSync(directory, { recursive: true, force: true });
}
