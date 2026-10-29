import assert from "node:assert/strict";
import http from "node:http";
import https from "node:https";
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";

const organization = process.env.KRUN_VERIFY_ORG;
const service = process.env.KRUN_VERIFY_SERVICE;
assert(organization, "KRUN_VERIFY_ORG is required");
assert(service, "KRUN_VERIFY_SERVICE is required");

const control = new URL(process.env.KRUN_CONTROL_ORIGIN ?? "http://127.0.0.1:8080");
const browserOrigin = process.env.KRUN_BROWSER_ORIGIN ?? "http://127.0.0.1:5173";
const gateway = new URL(process.env.KRUN_GATEWAY_ORIGIN ?? "http://127.0.0.1:38080");
const cookies = new Map();
let csrf = "";

function rawRequest(target, { method = "GET", headers = {}, body } = {}) {
  return new Promise((resolve, reject) => {
    const url = target instanceof URL ? target : new URL(target, control);
    const transport = url.protocol === "https:" ? https : http;
    const request = transport.request(
      url,
      {
        method,
        headers: {
          ...headers,
          ...(body === undefined ? {} : { "content-length": Buffer.byteLength(body) }),
        },
      },
      (response) => {
        const chunks = [];
        response.on("data", (chunk) => chunks.push(chunk));
        response.on("end", () => {
          for (const value of response.headers["set-cookie"] ?? []) {
            const [pair] = value.split(";", 1);
            const separator = pair.indexOf("=");
            cookies.set(pair.slice(0, separator), pair.slice(separator + 1));
          }
          const text = Buffer.concat(chunks).toString("utf8");
          let parsed = text;
          try { parsed = JSON.parse(text); } catch {}
          resolve({ status: response.statusCode, headers: response.headers, body: parsed, text });
        });
      },
    );
    request.on("error", reject);
    request.end(body);
  });
}

async function api(path, method = "GET", input, expected = 200) {
  const body = input === undefined ? undefined : JSON.stringify(input);
  const response = await rawRequest(new URL(path, control), {
    method,
    body,
    headers: {
      accept: "application/json",
      origin: browserOrigin,
      ...(body === undefined ? {} : { "content-type": "application/json" }),
      ...(cookies.size ? { cookie: [...cookies].map(([name, value]) => `${name}=${value}`).join("; ") } : {}),
      ...(csrf ? { "x-csrf-token": csrf } : {}),
    },
  });
  assert.equal(response.status, expected, `${method} ${path}: ${response.text}`);
  return response.body;
}

async function login() {
  csrf = (await api("/api/auth/csrf", "POST")).csrf_token;
  const form = new URLSearchParams({ return_to: "/" }).toString();
  const loginResponse = await rawRequest(new URL("/api/auth/local/login", control), {
    method: "POST",
    body: form,
    headers: {
      accept: "application/json",
      "content-type": "application/x-www-form-urlencoded",
      origin: browserOrigin,
      cookie: [...cookies].map(([name, value]) => `${name}=${value}`).join("; "),
      "x-csrf-token": csrf,
    },
  });
  assert.equal(loginResponse.status, 200, loginResponse.text);
  const callback = new URL(loginResponse.body.redirect_url, browserOrigin);
  const callbackResponse = await rawRequest(new URL(callback.pathname + callback.search, control), {
    headers: { cookie: [...cookies].map(([name, value]) => `${name}=${value}`).join("; ") },
  });
  assert.equal(callbackResponse.status, 303, callbackResponse.text);
  csrf = (await api("/api/auth/csrf", "POST")).csrf_token;
}

function tarFile(name, bytes) {
  const header = Buffer.alloc(512);
  header.write(name, 0, 100, "utf8");
  const octal = (offset, length, value) => header.write(value.toString(8).padStart(length - 1, "0") + "\0", offset, length, "ascii");
  octal(100, 8, 0o644); octal(108, 8, 0); octal(116, 8, 0); octal(124, 12, bytes.length); octal(136, 12, 0);
  header.fill(32, 148, 156); header[156] = 48; header.write("ustar\0", 257, 6, "ascii"); header.write("00", 263, 2, "ascii");
  const checksum = header.reduce((sum, byte) => sum + byte, 0);
  header.write(checksum.toString(8).padStart(6, "0") + "\0 ", 148, 8, "ascii");
  return Buffer.concat([header, bytes, Buffer.alloc((512 - bytes.length % 512) % 512)]);
}

const file = (name) => readFileSync(new URL("../" + name, import.meta.url));
const code = {
  "index.js": file("src/index.js"),
  "http.js": file("src/http.js"),
  "package.json": file("package.json"),
  "package-lock.json": file("package-lock.json"),
};
const assets = {
  "index.html": file("frontend/public/index.html"),
  "about.html": file("frontend/public/about.html"),
  "assets/app-a1b2c3d4.js": file("frontend/public/assets/app-a1b2c3d4.js"),
  "assets/styles-a1b2c3d4.css": file("frontend/public/assets/styles-a1b2c3d4.css"),
  "assets/krun-a1b2c3d4.svg": file("frontend/public/assets/krun-a1b2c3d4.svg"),
  // Local verification may run after its disposable MinIO was recreated while
  // PostgreSQL still contains content-addressed Artifact rows. A per-run marker
  // avoids incorrectly reusing metadata for an object that belonged to the old
  // disposable store; it is not referenced by the application.
  "verification.json": Buffer.from(JSON.stringify({ run: randomUUID() })),
};
const bundle = gzipSync(Buffer.concat([
  ...Object.entries(code).map(([name, bytes]) => tarFile("src/" + name, bytes)),
  ...Object.entries(assets).map(([name, bytes]) => tarFile("assets/" + name, bytes)),
  Buffer.alloc(1024),
]));

async function poll(path, terminal, timeout = 180_000) {
  const deadline = Date.now() + timeout;
  let value;
  while (Date.now() < deadline) {
    value = await api(path);
    if (terminal.includes(value.state)) return value;
    if (value.state === "failed") throw new Error(`${path} failed: ${value.error}`);
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`${path} timed out in state ${value?.state}`);
}

function gatewayRequest(host, path, { method = "GET", headers = {}, body } = {}) {
  return rawRequest(new URL(path, gateway), { method, body, headers: { ...headers, host } });
}

await login();
const base = `/api/v1/orgs/${organization}/services/${service}/workers`;
const upload = await api(base + "/source-uploads", "POST", {}, 201);
const uploaded = await rawRequest(new URL(upload.upload_url), {
  method: "PUT",
  body: bundle,
  headers: { "content-type": "application/gzip" },
});
assert.equal(uploaded.status, 200, uploaded.text);

const queuedBuild = await api(base + "/builds", "POST", {
  environment: "production",
  source_type: "upload",
  source_upload: upload.source_upload,
  entrypoint: "index.js",
  compatibility_date: "2026-09-28",
  compatibility_flags: ["nodejs_compat"],
  assets: {
    directory: "frontend/public",
    binding: "ASSETS",
    run_worker_first: ["/api/*"],
    not_found_handling: "single-page-application",
    html_handling: "auto-trailing-slash",
  },
  idempotency_key: randomUUID(),
}, 202);
const build = await poll(base + "/builds/" + queuedBuild.id, ["succeeded"]);
const artifact = await api(base + "/artifacts/" + build.artifact_id);
const environments = (await api(base + "/environments")).items;
const production = environments.find((item) => item.name === "production");
assert(production, "production environment is missing");
const queuedRelease = await api(base + "/environments/production/releases", "POST", {
  build_id: build.id,
  expected_revision: production.release_revision,
  idempotency_key: randomUUID(),
}, 202);
const operation = await poll(base + "/operations/" + queuedRelease.id, ["available"]);
let current = (await api(base + "/environments")).items.find((item) => item.name === "production");
const host = current.display_hosts?.internal ?? current.hosts?.[0] ?? current.host;
assert(host, "production host is missing");

const initialSecrets = await api(base + "/environments/production/secrets");
if (initialSecrets.items.some((item) => item.name === "VERIFICATION_SECRET")) {
  const queuedCleanup = await api(base + "/environments/production/secrets/VERIFICATION_SECRET", "DELETE", {
    expected_revision: current.release_revision,
    idempotency_key: randomUUID(),
  }, 202);
  await poll(base + "/operations/" + queuedCleanup.id, ["available"]);
  current = (await api(base + "/environments")).items.find((item) => item.name === "production");
}

const root = await gatewayRequest(host, "/");
assert.equal(root.status, 200); assert(root.text.includes('data-app="krun-workers-fullstack"'));
const health = await gatewayRequest(host, "/api/health");
assert.equal(health.status, 200); assert.equal(health.body.service, "krun-workers-fullstack");
assert.equal(health.body.secret_configured, false);
const assetBinding = await gatewayRequest(host, "/api/asset-check");
assert.equal(assetBinding.status, 200); assert.deepEqual(assetBinding.body, { status: 200, about_page: true });
const secretValue = "local-secret-" + randomUUID();
const queuedSecret = await api(base + "/environments/production/secrets/VERIFICATION_SECRET", "PUT", {
  value: secretValue,
  expected_revision: current.release_revision,
  idempotency_key: randomUUID(),
}, 202);
const secretOperation = await poll(base + "/operations/" + queuedSecret.id, ["available"]);
const secretRelease = await api(base + "/releases/" + secretOperation.release_id);
assert.equal(secretRelease.artifact_id, artifact.id);
const secretHealth = await gatewayRequest(host, "/api/health");
assert.equal(secretHealth.body.secret_configured, true);
assert.equal(secretHealth.body.release_id, secretOperation.release_id);
const secretList = await api(base + "/environments/production/secrets");
assert(secretList.items.some((item) => item.name === "VERIFICATION_SECRET"));
assert(!JSON.stringify(secretList).includes(secretValue));
const secretEnvironment = (await api(base + "/environments")).items.find((item) => item.name === "production");
const queuedDelete = await api(base + "/environments/production/secrets/VERIFICATION_SECRET", "DELETE", {
  expected_revision: secretEnvironment.release_revision,
  idempotency_key: randomUUID(),
}, 202);
const deleteOperation = await poll(base + "/operations/" + queuedDelete.id, ["available"]);
const deleteRelease = await api(base + "/releases/" + deleteOperation.release_id);
assert.equal(deleteRelease.artifact_id, artifact.id);
const deletedHealth = await gatewayRequest(host, "/api/health");
assert.equal(deletedHealth.body.secret_configured, false);
assert.equal(deletedHealth.body.release_id, deleteOperation.release_id);
const echo = await gatewayRequest(host, "/api/echo", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ message: "local full-stack verification" }),
});
assert.equal(echo.status, 200); assert.equal(echo.body.echo, "local full-stack verification");
const spa = await gatewayRequest(host, "/dashboard", { headers: { "sec-fetch-mode": "navigate" } });
assert.equal(spa.status, 200); assert(spa.text.includes('data-app="krun-workers-fullstack"'));
const script = await gatewayRequest(host, "/assets/app-a1b2c3d4.js");
assert.equal(script.status, 200); assert.match(script.headers["cache-control"], /immutable/);
const cached = await gatewayRequest(host, "/", { headers: { "if-none-match": root.headers.etag } });
assert.equal(cached.status, 304);
const workerDomain = host.split(".").slice(1).join(".");
const staticHost = "static." + workerDomain;
const staticPath = `/${organization}/${service}/${artifact.archive_sha256}/index.html`;
let staticPage;
for (let attempt = 0; attempt < 20; attempt++) {
  staticPage = await gatewayRequest(staticHost, staticPath);
  if (staticPage.status === 200) break;
  await new Promise((resolve) => setTimeout(resolve, 250));
}
assert.equal(staticPage.status, 200); assert(staticPage.text.includes('data-app="krun-workers-fullstack"'));
assert.equal(staticPage.headers["access-control-allow-origin"], "*");
const storageOrigin = new URL(upload.upload_url).origin;
const privateArchive = await rawRequest(new URL(
  `/krun-artifacts/v1/${organization}/sha256/${artifact.archive_sha256}/bundle.tar.gz`,
  storageOrigin,
));
assert.equal(privateArchive.status, 403);

console.log(JSON.stringify({
  state: "pass",
  organization,
  service,
  build: build.id,
  artifact: artifact.id,
  operation: operation.id,
  release: deleteOperation.release_id,
  host,
  checks: {
    root: root.status,
    health: health.status,
    assets_binding: assetBinding.status,
    echo: echo.status,
    spa: spa.status,
    immutable: script.headers["cache-control"],
    cached: cached.status,
    static_domain: staticPage.status,
    static_cors: staticPage.headers["access-control-allow-origin"],
    private_archive: privateArchive.status,
    secret_put_release: secretOperation.release_id,
    secret_delete_release: deleteOperation.release_id,
    secret_value_exposed: false,
  },
}, null, 2));
