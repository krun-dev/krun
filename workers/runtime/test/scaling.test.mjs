import { writeFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gzipSync } from "node:zlib";
import assert from "node:assert/strict";
import { loadRuntimeSource } from "./entry-source.mjs";
const directory = mkdtempSync(join(tmpdir(), "krun-scaling-"));
const source = loadRuntimeSource();
writeFileSync(
  join(directory, "entry.mjs"),
  source +
    `export { applySnapshot, applyBatch, resolveRoute, assetManifest };
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
  const runtime = await fresh();
  const count = 100000;
  runtime.applySnapshot({
    config_revision: 1,
    routes: Array.from({ length: count }, (_, i) => ({
      host: `site-${i}.test`,
      version: `version-${i}`,
    })),
    versions: Object.fromEntries(
      Array.from({ length: count }, (_, i) => [`version-${i}`, { id: `version-${i}` }]),
    ),
  });
  const before = runtime.inspect();
  // Iterating either entire index on an incremental update is a regression,
  // independently of machine speed and wall-clock benchmark variance.
  for (const index of [before.view.routes, before.view.versions]) {
    for (const method of [Symbol.iterator, "entries", "values", "keys"]) {
      index[method] = () => {
        throw new Error("incremental update scanned full index");
      };
    }
  }
  assert(
    runtime.applyBatch({
      from: 1,
      to: 2,
      ops: [
        { type: "version_put", version: { id: "new-version" } },
        {
          type: "route_upsert",
          host: "site-0.test",
          route: { host: "site-0.test", version: "new-version" },
        },
      ],
    }),
  );
  assert.equal(runtime.inspect().view.routes, before.view.routes);
  assert.equal(runtime.inspect().view.versions, before.view.versions);
  assert(!runtime.inspect().view.versions.has("version-0"));
  assert(runtime.inspect().view.versions.has("new-version"));
  assert.throws(() =>
    runtime.applyBatch({
      from: 2,
      to: 3,
      ops: [{ type: "route_delete", host: "site-0.test" }, { type: "broken" }],
    }),
  );
  assert(runtime.inspect().view.routes.has("site-0.test"));
  assert.equal(runtime.inspect().view.revision, 2);
  console.log("PASS bounded incremental update and malformed-batch atomicity at 100k routes");
  const lazy = await fresh();
  lazy.applySnapshot({
    pool: "default",
    config_revision: 1,
    lazy_routes: true,
  });
  let requests = 0;
  const env = environment(async (request) => {
    requests++;
    const host = decodeURIComponent(new URL(request.url).pathname.split("/").at(-1));
    return Response.json({
      pool: "default",
      config_revision: 2,
      routes: [{ host, version: host, enabled: true }],
      versions: { [host]: { id: host } },
    });
  });
  await Promise.all(Array.from({ length: 20 }, () => lazy.resolveRoute(env, "cold.test")));
  assert.equal(requests, 1);
  // A cold lookup can observe a newer revision than the following delta feed.
  lazy.applyBatch({
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
  const view = lazy.inspect();
  assert.equal(view.view.routes.size, 4096);
  assert.equal(view.view.versions.size, 4096);
  assert.equal(view.routeVersions.size, 4096);
  assert.equal(view.versionRefs.size, 4096);
  lazy.applyBatch({
    from: 2,
    to: 3,
    ops: [
      { type: "version_put", version: { id: "uncached" } },
      {
        type: "route_upsert",
        host: "never-visited.test",
        route: { host: "never-visited.test", version: "uncached" },
      },
    ],
  });
  assert(!view.view.routes.has("never-visited.test"));
  assert(!view.view.versions.has("uncached"));
  console.log(
    "PASS coalesced cold lookup, bounded route/descriptor caches and unloaded delta filtering",
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
  for (let i = 0; i < 600; i++) {
    await assets.assetManifest(assetEnv, { id: `version-${i}` });
    await assets.assetManifest(assetEnv, { id: "hot" });
  }
  assert.equal(assets.inspect().manifests.size, 512);
  const callsBefore = assetCalls;
  await assets.assetManifest(assetEnv, { id: "hot" });
  assert.equal(assetCalls, callsBefore);
  console.log("PASS single-flight manifest loading and LRU without whole-cache invalidation");
  const fallback = await fresh();
  fallback.applySnapshot({
    pool: "default",
    config_revision: 7,
    lazy_routes: true,
  });
  const snapshot = {
    pool: "default",
    config_revision: 7,
    routes: [{ host: "fallback.test", version: "fallback", enabled: true }],
    versions: { fallback: { id: "fallback" } },
  };
  const compressed = gzipSync(JSON.stringify(snapshot));
  const digest = Buffer.from(await crypto.subtle.digest("SHA-256", compressed)).toString("hex");
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url) =>
    (url instanceof Request ? url.url : String(url)).endsWith("/latest.json")
      ? Response.json({
          snapshot: "shards/fixture/7.json.gz",
          sha256: digest,
          config_revision: 7,
        })
      : new Response(compressed);
  try {
    const env = environment(async () => {
      throw new Error("control down");
    });
    env.CONFIG_BASE = "http://config.test";
    assert.equal((await fallback.resolveRoute(env, "fallback.test")).version, "fallback");
  } finally {
    globalThis.fetch = originalFetch;
  }
  console.log("PASS immutable shard fallback while control plane is unavailable");
} finally {
  rmSync(directory, { recursive: true, force: true });
}
