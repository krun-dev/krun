const source = `
let calls = 0;
let instance;
function compute(n, seed) {
  let x = seed | 0;
  for (let i = 0; i < n; i++) x = (Math.imul(x ^ i, 1664525) + 1013904223) | 0;
  return x;
}
export default {
  async fetch(request) {
    instance ??= crypto.randomUUID();
    calls++;
    const url = new URL(request.url);
    const headers = {"x-experiment-instance": instance};
    switch (url.pathname) {
      case "/cpu":
        return new Response(String(compute(36000000, calls)), {headers});
      case "/wait":
        await new Promise(resolve => setTimeout(resolve, 100));
        return new Response("wait", {headers});
      case "/echo":
        return new Response(request.body, {headers});
      case "/stream":
        return new Response(new ReadableStream({
          async start(controller) {
            const encoder = new TextEncoder();
            controller.enqueue(encoder.encode("first"));
            await new Promise(resolve => setTimeout(resolve, 100));
            controller.enqueue(encoder.encode("second"));
            controller.close();
          }
        }), {headers});
      default:
        return new Response("ok", {headers});
    }
  }
};
`;

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === "/__ready") return new Response("ready");
    const worker = await env.LOADER.get(url.hostname, () => ({
      compatibilityDate: "2026-09-28",
      mainModule: "worker.js",
      modules: { "worker.js": { js: source } },
      globalOutbound: null,
    }));
    return worker.getEntrypoint().fetch(request);
  },
};
