type Env = Record<string, string>;

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname !== "/headers") {
      return Response.json({ error: "not found" }, { status: 404 });
    }
    const headers: Record<string, string> = {};
    request.headers.forEach((value, key) => {
      headers[key] = value;
    });
    const cf = (request as Request & { cf?: unknown }).cf;
    return Response.json(
      {
        method: request.method,
        url: request.url,
        headers,
        cf: cf ?? null,
        environment: {
          KRUN_ENVIRONMENT: env.KRUN_ENVIRONMENT ?? null,
          KRUN_SERVICE_ID: env.KRUN_SERVICE_ID ?? null,
          KRUN_RELEASE_ID: env.KRUN_RELEASE_ID ?? null,
          GREETING: env.GREETING ?? null,
        },
        received_at: new Date().toISOString(),
      },
      { headers: { "cache-control": "no-store" } },
    );
  },
};
