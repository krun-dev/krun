import { loadRuntimeSource } from "./entry-source.mjs";
import { performance } from "node:perf_hooks";
const source = loadRuntimeSource() + "\nexport {bootstrapFromControl, resolveRoute, applyBatch};";
const runtime = await import(
  "data:text/javascript;base64," + Buffer.from(source).toString("base64")
);
const env = {
  RUNTIME_POOL: "default",
  RUNTIME_TOKEN: "fixture",
  PLATFORM: {
    async fetch(request) {
      const path = new URL(request.url).pathname;
      if (path.endsWith("/config/snapshot")) {
        return Response.json({ pool: "default", config_revision: 1, lazy_routes: true });
      }
      const host = decodeURIComponent(path.split("/").at(-1));
      return Response.json({
        pool: "default",
        config_revision: 1,
        routes: [{ host, version: host, enabled: true }],
        versions: { [host]: { id: host } },
      });
    },
  },
};
console.log(
  JSON.stringify({
    node: process.version,
    kind: "single-route applyBatch with a warm lazy cache; excludes network and workerd",
  }),
);
for (const n of [64, 512, 4096]) {
  await runtime.bootstrapFromControl(env);
  for (let i = 0; i < n; i++) {
    await runtime.resolveRoute(env, `s${i}.example.invalid`);
  }
  const samples = [];
  for (let i = 0; i < 3; i++) {
    globalThis.gc?.();
    const start = performance.now();
    if (
      !runtime.applyBatch({
        from: i + 1,
        to: i + 2,
        ops: [
          {
            type: "route_upsert",
            host: "s0.example.invalid",
            route: {
              host: "s0.example.invalid",
              version: "s0.example.invalid",
              enabled: true,
            },
          },
        ],
      })
    ) {
      throw new Error("batch rejected");
    }
    samples.push(Number((performance.now() - start).toFixed(3)));
  }
  console.log(JSON.stringify({ routes: n, versions: n, single_route_update_ms: samples }));
}
