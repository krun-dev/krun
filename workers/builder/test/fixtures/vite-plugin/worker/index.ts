export default {
  fetch(request: Request): Response {
    const url = new URL(request.url);
    if (url.pathname === "/api/fixture") {
      return Response.json({source: "cloudflare-vite-plugin"});
    }
    return new Response("vite-worker");
  },
};
