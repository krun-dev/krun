import { json, methodNotAllowed } from "./http.js";

const service = "krun-workers-fullstack";

async function health(request, env) {
  if (request.method !== "GET") return methodNotAllowed("GET");
  return json({
    ok: true,
    service,
    environment: env.KRUN_ENVIRONMENT ?? "local-test",
    release_id: env.KRUN_RELEASE_ID ?? "local-test",
  });
}

async function echo(request, env) {
  if (request.method !== "POST") return methodNotAllowed("POST");
  let input;
  try {
    input = await request.json();
  } catch {
    return json({ error: "invalid_json" }, { status: 400 });
  }
  if (typeof input?.message !== "string" || input.message.length > 200) {
    return json(
      { error: "invalid_message", message: "message must be a string up to 200 characters" },
      { status: 422 },
    );
  }
  return json({
    ok: true,
    echo: input.message,
    environment: env.KRUN_ENVIRONMENT ?? "local-test",
  });
}

export async function handleRequest(request, env = {}) {
  const url = new URL(request.url);
  if (url.pathname === "/api/health") return health(request, env);
  if (url.pathname === "/api/echo") return echo(request, env);
  return json({ error: "not_found", path: url.pathname }, { status: 404 });
}

export default { fetch: handleRequest };
