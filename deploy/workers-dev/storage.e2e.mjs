// Runs against local-first with a real object store, local builder and workerd.
// Credentials stay in the main YAML; this harness only uses a short-lived API key.
import assert from "node:assert/strict";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { request as httpRequest } from "node:http";
import { randomUUID } from "node:crypto";
import { chromium, expect } from "@playwright/test";
const origin = process.env.KRUN_UI_ORIGIN;
const gateway = process.env.KRUN_DEV_GATEWAY_ORIGIN;
const staticHost = process.env.KRUN_DEV_STATIC_HOST;
const prefix = process.env.KRUN_STORAGE_PREFIX || "dev/run";
assert(
  origin && gateway && staticHost,
  "Set KRUN_UI_ORIGIN, KRUN_DEV_GATEWAY_ORIGIN and KRUN_DEV_STATIC_HOST",
);
const directory = process.env.KRUN_STORAGE_E2E_DIR || ".data/dev-storage-e2e";
mkdirSync(directory, { recursive: true, mode: 0o700 });
const cookies = new Map();
const results = [];
let csrf = "";
let token = "";
async function api(path, method = "GET", body, session = false, status = 200) {
  const headers = { accept: "application/json" };
  if (session) {
    headers.cookie = [...cookies].map(([k, v]) => k + "=" + v).join("; ");
    headers.origin = origin;
    if (csrf) {
      headers["x-csrf-token"] = csrf;
    }
  } else if (token) {
    headers.authorization = "Bearer " + token;
  }
  if (body !== undefined) {
    headers["content-type"] =
      typeof body === "string" ? "application/x-www-form-urlencoded" : "application/json";
  }
  const response = await fetch(origin + path, {
    method,
    headers,
    body: body === undefined ? undefined : typeof body === "string" ? body : JSON.stringify(body),
    redirect: "manual",
    signal: AbortSignal.timeout(180000),
  });
  for (const item of response.headers.getSetCookie()) {
    const [pair] = item.split(";");
    const at = pair.indexOf("=");
    cookies.set(pair.slice(0, at), pair.slice(at + 1));
  }
  const raw = await response.text();
  let value;
  try {
    value = JSON.parse(raw);
  } catch {
    value = raw;
  }
  assert.equal(response.status, status, method + " " + path + " failed");
  return value;
}
async function check(name, fn) {
  await fn();
  results.push({ name, state: "pass" });
  writeFileSync(directory + "/results.json", JSON.stringify(results, null, 2));
  console.log("PASS " + name);
}
async function eventually(fn) {
  let error;
  for (let i = 0; i < 360; i++) {
    try {
      return await fn();
    } catch (e) {
      error = e;
      await new Promise((r) => setTimeout(r, 500));
    }
  }
  throw error;
}
async function poll(path, state) {
  return eventually(async () => {
    const value = await api(path);
    assert.equal(value.state, state, value.error || value.state);
    return value;
  });
}
async function invoke(host, path = "/", headers = {}) {
  return new Promise((resolve, reject) => {
    const req = httpRequest(
      new URL(path, gateway),
      { headers: { ...headers, host } },
      (response) => {
        let body = "";
        response.setEncoding("utf8");
        response.on("data", (s) => (body += s));
        response.on("end", () =>
          resolve({ status: response.statusCode, headers: response.headers, body }),
        );
      },
    );
    req.on("error", reject);
    req.setTimeout(15000, () => req.destroy(Error("gateway timeout")));
    req.end();
  });
}
function bundle(code, assets) {
  const tar = (name, text) => {
    const bytes = Buffer.from(text);
    const h = Buffer.alloc(512);
    h.write(name);
    const oct = (o, n, v) => h.write(v.toString(8).padStart(n - 1, "0") + "\0", o, n);
    oct(100, 8, 0o644);
    oct(108, 8, 0);
    oct(116, 8, 0);
    oct(124, 12, bytes.length);
    oct(136, 12, 0);
    h.fill(32, 148, 156);
    h[156] = 48;
    h.write("ustar\0", 257);
    h.write("00", 263);
    h.write(
      h
        .reduce((n, b) => n + b, 0)
        .toString(8)
        .padStart(6, "0") + "\0 ",
      148,
    );
    return Buffer.concat([h, bytes, Buffer.alloc((512 - (bytes.length % 512)) % 512)]);
  };
  return gzipSync(
    Buffer.concat([
      ...Object.entries(code).map(([p, b]) => tar("src/" + p, b)),
      ...Object.entries(assets).map(([p, b]) => tar("assets/" + p, b)),
      Buffer.alloc(1024),
    ]),
  );
}
csrf = (await api("/api/auth/csrf", "POST", undefined, true)).csrf_token;
const login = await api("/api/auth/local/login", "POST", "return_to=%2F", true);
const callback = new URL(login.redirect_url, origin);
await api(callback.pathname + callback.search, "GET", undefined, true, 303);
csrf = (await api("/api/auth/csrf", "POST", undefined, true)).csrf_token;
token = (
  await api(
    "/api/v1/system/api-keys",
    "POST",
    {
      name: "Storage integration verification",
      expires_in: 3600,
      policy: {
        schema_version: 1,
        statements: [
          { effect: "allow", actions: ["*"], resource: { kind: "*", all_in_system: true } },
        ],
      },
    },
    true,
    201,
  )
).key;
const org = await api(
  "/api/v1/orgs",
  "POST",
  { name: "Storage integration " + randomUUID().slice(0, 8) },
  true,
  201,
);
const site = await api(
  "/api/v1/orgs/" + org.id + "/services",
  "POST",
  { name: "Native storage fixture" },
  false,
  201,
);
const base = "/api/v1/orgs/" + org.id + "/services/" + site.id + "/workers";
const profiles = (await api("/api/v1/orgs/" + org.id + "/workers/network-profiles")).items;
await api(base + "/network-profile", "PUT", {
  network_profile: profiles.find((p) => p.default)?.id || profiles[0].id,
  expected_revision: 0,
});
let build;
let artifact;
let host;
await check("native source upload and local build use the configured private prefix", async () => {
  const upload = await api(base + "/source-uploads", "POST", {}, false, 201);
  assert(new URL(upload.upload_url).pathname.includes("/" + prefix + "/uploads/"));
  const code = {
    "index.ts": readFileSync(new URL("./fixtures/storage-worker.ts", import.meta.url), "utf8"),
  };
  const assets = {
    "index.html":
      "<!doctype html><html><title>Private storage fixture</title><h1>Private storage works</h1></html>",
    "hello.txt": "private asset bytes",
    _headers: "/*\n X-Storage-Fixture: yes",
  };
  const put = await fetch(upload.upload_url, { method: "PUT", body: bundle(code, assets) });
  assert.equal(put.status, 200);
  build = await api(
    base + "/builds",
    "POST",
    {
      environment: "production",
      source_type: "upload",
      source_upload: upload.source_upload,
      entrypoint: "index.ts",
      assets: {
        directory: "public",
        binding: "ASSETS",
        run_worker_first: ["/api/*"],
        not_found_handling: "none",
        html_handling: "auto-trailing-slash",
      },
      idempotency_key: randomUUID(),
    },
    false,
    202,
  );
  build = await poll(base + "/builds/" + build.id, "succeeded");
  artifact = await api(base + "/artifacts/" + build.artifact_id);
  assert(artifact.object_key.startsWith("v1/"));
  const source = await api(base + "/builds/" + build.id + "/source");
  assert(new URL(source.download_url).pathname.includes("/" + prefix + "/uploads/"));
});
await check("publish validates the private archive and converges on serving runtime", async () => {
  const env = (await api(base + "/environments")).items.find((e) => e.name === "production");
  const operation = await api(
    base + "/environments/production/deployments",
    "POST",
    {
      build_id: build.id,
      expected_revision: env.deployment_revision,
      idempotency_key: randomUUID(),
    },
    false,
    202,
  );
  await poll(base + "/operations/" + operation.id, "available");
  const current = (await api(base + "/environments")).items.find((e) => e.name === "production");
  host = current.display_hosts?.internal || current.hosts?.[current.domains.indexOf("internal")];
  assert(host);
  const response = await invoke(host, "/");
  assert.equal(response.status, 200);
  assert(response.body.includes("Private storage works"));
  assert.equal(response.headers["x-storage-fixture"], "yes");
});
await check(
  "ASSETS binding works and business Worker receives no storage credentials",
  async () => {
    const bound = await invoke(host, "/api/assets");
    assert.equal(bound.status, 200);
    assert.equal(bound.body, "private asset bytes");
    const worker = JSON.parse((await invoke(host, "/api/env")).body);
    assert.equal(worker.message, "private-storage-worker");
    assert(
      !worker.bindings.some((key) =>
        [
          "STORAGE_ACCESS_KEY",
          "STORAGE_SECRET_KEY",
          "STORAGE_REGION",
          "RUNTIME_TOKEN",
          "PLATFORM",
        ].includes(key),
      ),
    );
    const network = JSON.parse((await invoke(host, "/api/network")).body);
    assert.equal(network.httpBlocked, true);
    assert.equal(network.tcpBlocked, true);
  },
);
const staticPath = "/" + org.id + "/" + site.id + "/" + artifact.archive_sha256 + "/index.html";
await check(
  "static domain reads a private object and preserves ETag while rejecting code paths",
  async () => {
    const response = await eventually(async () => {
      const r = await invoke(staticHost, staticPath);
      assert.equal(r.status, 200);
      return r;
    });
    assert(response.body.includes("Private storage works"));
    assert.equal(
      (await invoke(staticHost, staticPath, { "if-none-match": response.headers.etag })).status,
      304,
    );
    assert.equal((await invoke(staticHost, "/v1/private/bundle.tar.gz")).status, 403);
    assert.equal(
      (await invoke(staticHost, staticPath.replace("index.html", "missing.txt"))).status,
      404,
    );
  },
);
await check("browser opens the real console and invokes the deployed Worker", async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext();
    const endpoint = new URL(origin);
    await context.addCookies(
      [...cookies].map(([name, value]) => ({ name, value, domain: endpoint.hostname, path: "/" })),
    );
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto(origin + "/?page=workers&workspace=" + org.id + "&worker=" + site.id);
    await page.getByRole("tab", { name: "调用测试", exact: true }).click();
    await page.getByRole("textbox", { name: "请求路径", exact: true }).fill("/api/env");
    await page.getByRole("button", { name: "发送请求", exact: true }).click();
    await expect(page.getByText(/private-storage-worker/)).toBeVisible();
    assert.deepEqual(errors, []);
    await page.screenshot({ path: directory + "/console.png", fullPage: true });
  } finally {
    await browser.close();
  }
});
writeFileSync(
  directory + "/fixture.json",
  JSON.stringify(
    { organization: org.id, service: site.id, host, staticHost, staticPath, artifact: artifact.id },
    null,
    2,
  ),
);
console.log("LOCAL STORAGE E2E PASSED " + results.length + " cases");
