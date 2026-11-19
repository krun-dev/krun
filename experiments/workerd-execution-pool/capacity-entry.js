const source = `
let instance;
let resident;
let calls = 0;

function compute(seed, rounds) {
  let x = seed | 0;
  for (let i = 0; i < rounds; i++) x = (Math.imul(x ^ i, 1664525) + 1013904223) | 0;
  return x;
}

export default {
  async fetch(request) {
    const url = new URL(request.url);
    const [, storage, mib, id, action] = url.pathname.split("/");
    const cold = instance === undefined;
    if (cold) {
      instance = crypto.randomUUID();
      // A packed array of small integers uses 4-byte slots in the compression cage.
      resident = storage === "heap" ? new Array(Number(mib) * 256 * 1024)
                                    : new Uint8Array(Number(mib) * 1024 * 1024);
      resident.fill(Number(id) % 255 + 1);
    }
    const seed = (resident[0] ?? 0) + ++calls;
    const rounds = Math.min(36000000, Number(url.searchParams.get("rounds")) || 36000000);
    const delay = Math.min(5000, Number(url.searchParams.get("workerDelayMs")) || 0);
    if (delay > 0) await scheduler.wait(delay);
    return new Response(String(action === "cpu" ? compute(seed, rounds) : seed), {
      headers: {"x-experiment-instance": instance, "x-experiment-cold": cold ? "1" : "0"},
    });
  },
};
`;

export default {
  async fetch(request, env) {
    const {hostname, pathname, searchParams} = new URL(request.url);
    if (pathname === "/__ready") return new Response("ready");
    const match = /^\/(buffer|heap)\/(0|1|3)\/(\d+)\/(read|cpu)$/.exec(pathname);
    if (!match) return new Response("invalid capacity request", {status: 400});
    const [, storage, mib, id] = match;
    let denied = false;
    try {
      const worker = await env.LOADER.get(`${hostname}:${storage}:${mib}:${id}`, async () => {
        if (request.headers.get("x-krun-lab-load") === "0") {
          denied = true;
          throw new Error("cold load needs a reservation");
        }
        const delay = Math.min(5000, Number(searchParams.get("loadDelayMs")) || 0);
        if (delay > 0) await scheduler.wait(delay);
        return {
          compatibilityDate: "2026-09-28",
          mainModule: "worker.js",
          modules: {"worker.js": {js: `// ${id}\n${source}`}},
          globalOutbound: null,
        };
      });
      if (request.headers.has("x-krun-lab-load")) {
        const businessHeaders = new Headers(request.headers);
        businessHeaders.delete("x-krun-lab-load");
        return await worker.getEntrypoint().fetch(new Request(request, {headers: businessHeaders}));
      }
      return await worker.getEntrypoint().fetch(request);
    } catch (error) {
      if (denied) return new Response(null, {
        status: 503,
        headers: {"x-krun-lab-cold-miss": "1"},
      });
      throw error;
    }
  },
};
