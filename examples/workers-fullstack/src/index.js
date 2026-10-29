import { Hono } from "hono";
import { createHash } from "node:crypto";
import { json, methodNotAllowed } from "./http.js";

const service = "krun-workers-fullstack";
const serviceDigest = createHash("sha256").update(service).digest("hex");

async function health(request, env) {
  if (request.method !== "GET") return methodNotAllowed("GET");
  const response = json({
    ok: true,
    service,
    environment: env.KRUN_ENVIRONMENT ?? "local-test",
    release_id: env.KRUN_RELEASE_ID ?? "local-test",
    secret_configured:
      typeof env.VERIFICATION_SECRET === "string" &&
      env.VERIFICATION_SECRET.length > 0,
  });
  response.headers.set("x-service-digest", serviceDigest);
  return response;
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

async function assetCheck(request, env) {
  if (!env.ASSETS || typeof env.ASSETS.fetch !== "function") {
    return json({ error: "assets_binding_unavailable" }, { status: 503 });
  }
  const target = new URL("/about", request.url);
  const response = await env.ASSETS.fetch(new Request(target));
  const body = await response.text();
  return json({ status: response.status, about_page: body.includes("STATIC HTML") });
}

const app = new Hono();
app.get("/api/health", (c) => health(c.req.raw, c.env));
app.all("/api/health", () => methodNotAllowed("GET"));
app.post("/api/echo", (c) => echo(c.req.raw, c.env));
app.all("/api/echo", () => methodNotAllowed("POST"));
app.get("/api/asset-check", (c) => assetCheck(c.req.raw, c.env));
app.all("/api/asset-check", () => methodNotAllowed("GET"));
app.notFound((c) => json({ error: "not_found", path: c.req.path }, { status: 404 }));

export function handleRequest(request, env = {}, ctx) {
  return app.fetch(request, env, ctx);
}

export default app;
