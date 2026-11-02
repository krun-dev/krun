import { readFileSync } from "node:fs";
import { performance } from "node:perf_hooks";
const source =
  readFileSync(new URL("../entry.js", import.meta.url), "utf8").replace(
    "import {WorkerEntrypoint} from 'cloudflare:workers';",
    "class WorkerEntrypoint {}",
  ) + "\nexport {applySnapshot,applyBatch};";
const runtime = await import(
  "data:text/javascript;base64," + Buffer.from(source).toString("base64")
);
console.log(
  JSON.stringify({
    node: process.version,
    kind: "actual applyBatch; minimal synthetic descriptors; not production workerd throughput",
  }),
);
for (const n of [10000, 100000, 1000000]) {
  let routes = Array.from({ length: n }, (_, i) => ({
    host: `s${i}.example.invalid`,
    release: `r${i}`,
    pool: "default",
    enabled: true,
  }));
  let releases = Object.fromEntries(
    routes.map((r, i) => [r.release, { id: r.release, service_id: `s${i}` }]),
  );
  runtime.applySnapshot({
    config_revision: 1,
    pool: "default",
    routes,
    releases,
  });
  routes = null;
  releases = null;
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
              release: "r0",
              pool: "default",
              enabled: true,
            },
          },
        ],
      })
    )
      throw new Error("batch rejected");
    samples.push(Number((performance.now() - start).toFixed(3)));
  }
  console.log(
    JSON.stringify({ routes: n, releases: n, single_route_update_ms: samples }),
  );
}
