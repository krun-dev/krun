// Run against the isolated kind backend through its same-origin console entry.
// All generated projects, CLI logs and credentials stay under ignored .data/.
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { Agent, request as httpRequest } from "node:http";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { createHash, randomUUID } from "node:crypto";
const origin = process.env.KRUN_UI_ORIGIN;
assert(origin, "KRUN_UI_ORIGIN must identify the isolated same-origin E2E console");
const directory = path.resolve(process.env.KRUN_CF_E2E_DIR || ".data/cf-api-e2e");
mkdirSync(directory, { recursive: true });
const cookies = new Map();
let csrf = "";
let token = "";
const results = [];
async function api(route, method = "GET", body, session = false, status = 200) {
  const headers = { accept: "application/json" };
  if (body !== undefined) {
    headers["content-type"] =
      typeof body === "string" ? "application/x-www-form-urlencoded" : "application/json";
  }
  if (session) {
    headers.cookie = [...cookies].map(([k, v]) => k + "=" + v).join("; ");
    headers.origin = origin;
    if (csrf) {
      headers["x-csrf-token"] = csrf;
    }
  } else if (token) {
    headers.authorization = "Bearer " + token;
  }
  const response = await fetch(origin + route, {
    method,
    headers,
    body: body === undefined ? undefined : typeof body === "string" ? body : JSON.stringify(body),
    redirect: "manual",
    signal: AbortSignal.timeout(180000),
  });
  for (const c of response.headers.getSetCookie()) {
    const [pair] = c.split(";");
    const at = pair.indexOf("=");
    cookies.set(pair.slice(0, at), pair.slice(at + 1));
  }
  const raw = await response.text();
  let out;
  try {
    out = JSON.parse(raw);
  } catch {
    out = raw;
  }
  assert.equal(response.status, status, method + " " + route + " " + JSON.stringify(out));
  return out;
}
async function check(name, fn) {
  await fn();
  results.push({ name, state: "pass" });
  writeFileSync(path.join(directory, "cloudflare-results.json"), JSON.stringify(results, null, 2));
  console.log("PASS " + name);
}
const auth = await api("/api/auth/csrf", "POST", undefined, true);
csrf = auth.csrf_token;
const login = await api(
  "/api/auth/local/login",
  "POST",
  new URLSearchParams({ return_to: "/" }).toString(),
  true,
);
const callback = new URL(login.redirect_url, origin);
await api(callback.pathname + callback.search, "GET", undefined, true, 303);
const loginUser = await api("/api/auth/me", "GET", undefined, true);
if (!loginUser.system_admin) {
  const kubeconfig = process.env.KUBECONFIG;
  const cluster = process.env.KIND_CLUSTER_NAME;
  assert(kubeconfig && cluster, "Use the isolated kind kubeconfig and cluster name");
  const kubectl = (...args) =>
    execFileSync("kubectl", ["--kubeconfig", kubeconfig, ...args], {
      encoding: "utf8",
      timeout: 30000,
    });
  assert.equal(kubectl("config", "current-context").trim(), "kind-" + cluster);
  kubectl(
    "exec",
    "-n",
    "workers-mvp",
    "deploy/backend",
    "-c",
    "backend",
    "--",
    "/app/krun-server",
    "bootstrap",
    "--config",
    "/config/control.yaml",
    "--user",
    loginUser.user.user_id,
  );
}
csrf = (await api("/api/auth/csrf", "POST", undefined, true)).csrf_token;
const credential = await api(
  "/api/v1/system/api-keys",
  "POST",
  {
    name: "CF protocol verification",
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
);
token = credential.key;
const locatorSuffix = randomUUID().slice(0, 8);
const org = await api(
  "/api/v1/orgs",
  "POST",
  { name: "协议验收 " + locatorSuffix, short_name: "CF Locator " + locatorSuffix },
  true,
  201,
);
assert.equal(org.short_name, "cf-locator-" + locatorSuffix);
const script = "tu-pian-chu-li-" + locatorSuffix;
const orgNative = "/api/v1/orgs/" + org.short_name;
const native = orgNative + "/workers/" + script;
const nativeWorker = await api(
  orgNative + "/workers",
  "POST",
  { name: "图片处理 " + locatorSuffix },
  false,
  201,
);
assert.equal(nativeWorker.locator, script);
const cf = "/api/v1/cloudflare/client/v4/accounts/" + org.id;
const scriptPath = cf + "/workers/scripts/" + script;
const workerPath = cf + "/workers/workers/" + script;
const config = path.join(directory, "wrangler.json");
const source = path.join(directory, "index.js");
writeFileSync(
  config,
  JSON.stringify({
    name: script,
    main: "index.js",
    compatibility_date: "2026-09-28",
    workers_dev: true,
    preview_urls: true,
    vars: { label: "production" },
    previews: { vars: { label: "preview" } },
  }),
);
function code(marker) {
  writeFileSync(
    source,
    `export default {
      fetch(request, env) {
        return Response.json({
          marker: ${JSON.stringify(marker)},
          label: env.label,
          secret: env.apiToken ?? null,
          base: env.baseToken ?? null,
        });
      },
    };`,
  );
}
let commandCount = 0;
async function wrangler(args, input = "", account = org.short_name) {
  const env = {
    ...process.env,
    CI: "true",
    CLOUDFLARE_API_BASE_URL: origin + "/api/v1/cloudflare/client/v4",
    CLOUDFLARE_API_TOKEN: token,
    CLOUDFLARE_ACCOUNT_ID: account,
    WRANGLER_SEND_METRICS: "false",
    WRANGLER_LOG_PATH: path.join(directory, "wrangler-private.log"),
  };
  const child = spawn(
    process.execPath,
    [
      path.resolve("workers/builder/node_modules/wrangler/bin/wrangler.js"),
      ...args,
      "--config",
      config,
    ],
    { env, stdio: ["pipe", "pipe", "pipe"] },
  );
  let log = "";
  let stdout = "";
  child.stdout.on("data", (b) => {
    stdout += b;
    log += b;
  });
  child.stderr.on("data", (b) => (log += b));
  child.stdin.end(input);
  const timer = setTimeout(() => child.kill("SIGTERM"), 180000);
  const status = await new Promise((resolve) => child.on("exit", resolve));
  clearTimeout(timer);
  log = log.split(token).join("[redacted]");
  writeFileSync(path.join(directory, "command-" + ++commandCount + ".log"), log, { mode: 0o600 });
  assert.equal(status, 0, args.join(" ") + " failed: " + log.slice(-6000));
  return args.includes("--json") ? stdout : log;
}
async function invoke(environment, expected) {
  let last;
  for (let i = 0; i < 60; i++) {
    last = await api(native + "/environments/" + environment + "/invoke", "POST", {
      path: "/",
      method: "GET",
    });
    if (last.status === 200) {
      let out;
      try {
        out = JSON.parse(last.body);
      } catch {}
      if (out && Object.entries(expected).every(([k, v]) => out[k] === v)) {
        return out;
      }
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  assert.fail("runtime did not converge: " + JSON.stringify(last));
}
let firstVersion;
let secondVersion;
await check("official Wrangler deploy uploads modules and publishes production", async () => {
  code("first");
  const output = await wrangler(["deploy"], "", org.id);
  const details = (await api(workerPath)).result;
  assert.equal(details.tag, nativeWorker.id);
  await invoke("production", { marker: "first", label: "production" });
  firstVersion = (await api(scriptPath + "/versions")).result.items[0].id;
  assert.match(firstVersion, /^[0-9a-f-]{36}$/);
  assert(output.includes("Current Version ID: " + firstVersion));
});
await check(
  "official Wrangler deploy by organization short name updates the same native Worker",
  async () => {
    const first = firstVersion;
    code("short-name");
    const output = await wrangler(["deploy"], "", org.short_name);
    const alias = "/api/v1/cloudflare/client/v4/accounts/" + org.short_name;
    const details = (await api(alias + "/workers/workers/" + script)).result;
    assert.equal(details.tag, nativeWorker.id);
    assert.equal((await api(native)).id, nativeWorker.id);
    const versions = (await api(scriptPath + "/versions")).result.items;
    assert.equal(versions[1].id, first);
    firstVersion = versions[0].id;
    assert.notEqual(firstVersion, first);
    assert(output.includes("Current Version ID: " + firstVersion));
    await invoke("production", { marker: "short-name", label: "production" });
  },
);
await check("official versions upload leaves production on its previous version", async () => {
  code("second");
  await wrangler(["versions", "upload"]);
  const versions = (await api(scriptPath + "/versions")).result.items;
  secondVersion = versions[0].id;
  assert.notEqual(firstVersion, secondVersion);
  await invoke("production", { marker: "short-name" });
});
await check("ordinary Secret mutation rejects an unpublished latest version", async () => {
  const response = await api(
    scriptPath + "/secrets",
    "PUT",
    { name: "apiToken", type: "secret_text", text: "fixture-secret" },
    false,
    409,
  );
  assert.equal(response.errors[0].code, 10215);
});
await check("official versions deploy selects one immutable version at 100 percent", async () => {
  await wrangler(["versions", "deploy", secondVersion + "@100%", "--yes"]);
  await invoke("production", { marker: "second" });
});
await check("official Secret put and list publish a masked secret snapshot", async () => {
  await wrangler(["secret", "put", "apiToken"], "fixture-secret\n");
  const list = await wrangler(["secret", "list"]);
  assert(list.includes("apiToken"));
  assert(!list.includes("fixture-secret"));
  await invoke("production", { marker: "second", secret: "fixture-secret" });
});
await check("official Previews Base secret is isolated from production", async () => {
  await wrangler(["preview", "base-config", "secret", "put", "baseToken"], "fixture-base\n");
  const list = await wrangler(["preview", "base-config", "secret", "list"]);
  assert(list.includes("baseToken"));
  assert(!list.includes("fixture-base"));
  await invoke("production", { base: null });
});
await check(
  "official Wrangler Preview copies Base bindings and deploys independently",
  async () => {
    code("preview");
    await wrangler(["preview", "--name", "branch-one"]);
    await invoke("branch-one", {
      marker: "preview",
      label: "preview",
      base: "fixture-base",
      secret: null,
    });
    await invoke("production", { marker: "second", secret: "fixture-secret", base: null });
  },
);
await check("official Preview Secret creates a new Preview deployment", async () => {
  await wrangler(
    ["preview", "secret", "put", "apiToken", "--name", "branch-one"],
    "fixture-preview\n",
  );
  await invoke("branch-one", { marker: "preview", secret: "fixture-preview" });
  await invoke("production", { secret: "fixture-secret" });
});
await check("official Preview delete removes only its target", async () => {
  await wrangler(["preview", "delete", "--name", "branch-one", "--skip-confirmation"]);
  await api(workerPath + "/previews/branch-one", "GET", undefined, false, 404);
  await invoke("production", { marker: "second" });
});
await check(
  "first Secret on a nonexistent Worker follows Wrangler placeholder deployment",
  async () => {
    const other = "cf-secret-" + randomUUID().slice(0, 8);
    await wrangler(["secret", "put", "initialToken", "--name", other], "fixture-initial\n");
    const list = await api(cf + "/workers/scripts/" + other + "/secrets");
    assert(list.result.some((b) => b.name === "initialToken"));
    await api(cf + "/workers/scripts/" + other, "DELETE");
  },
);
// Bindings must survive the complete official upload/deploy/rollback path.
let bindingsVersion;
let bindingsConfig;
const gatewayAgent = new Agent({ keepAlive: true, maxSockets: 32 });
async function workerGateway(worker, environment, domain, gatewayOrigin) {
  assert(gatewayOrigin, "Expose the " + domain + " test Gateway");
  const env = (await api(worker + "/environments")).items.find((e) => e.name === environment);
  const host = env.display_hosts?.[domain] || env.hosts?.[(env.domains || []).indexOf(domain)];
  assert(host, environment + " " + domain + " host");
  return { host, origin: gatewayOrigin };
}
function gatewayRequest(gateway, url, headers = {}, method = "GET", body = null) {
  return new Promise((resolve, reject) => {
    const req = httpRequest(
      new URL(url, gateway.origin),
      { agent: gatewayAgent, method, headers: { ...headers, host: gateway.host } },
      (response) => {
        let received = "";
        response.setEncoding("utf8");
        response.on("data", (chunk) => (received += chunk));
        response.on("error", reject);
        response.on("end", () =>
          resolve({ status: response.statusCode, body: received, headers: response.headers }),
        );
      },
    );
    req.on("error", reject);
    req.setTimeout(15000, () => req.destroy(new Error("gateway timeout")));
    req.end(body);
  });
}
async function invokePath(url, environment = "production") {
  if (!process.env.KRUN_CF_E2E_GATEWAY_ORIGIN) {
    return api(native + "/environments/" + environment + "/invoke", "POST", {
      path: url,
      method: "GET",
    });
  }
  const gateway = await workerGateway(
    native,
    environment,
    "internal",
    process.env.KRUN_CF_E2E_GATEWAY_ORIGIN,
  );
  return gatewayRequest(gateway, url);
}
await check(
  "asset capabilities reject corrupt files, incomplete tokens and cross-Worker reuse",
  async () => {
    const started = (
      await api(scriptPath + "/assets-upload-session", "POST", {
        manifest: { "/fixture.txt": { hash: "0".repeat(32), size: 4 } },
      })
    ).result;
    const form = new FormData();
    form.append("0".repeat(32), new Blob([Buffer.from("bad!").toString("base64")]), "file");
    const response = await fetch(origin + cf + "/workers/assets/upload?base64=true", {
      method: "POST",
      headers: { authorization: "Bearer " + started.jwt },
      body: form,
    });
    assert.equal(response.status, 422);
    function deployment(jwt) {
      const form = new FormData();
      form.append(
        "metadata",
        JSON.stringify({
          main_module: "index.js",
          compatibility_date: "2026-09-28",
          assets: { jwt },
        }),
      );
      form.append(
        "index.js",
        new Blob(['export default {fetch(){return new Response("ok")}}'], {
          type: "application/javascript+module",
        }),
        "index.js",
      );
      return form;
    }
    const incomplete = await fetch(origin + scriptPath, {
      method: "PUT",
      headers: { authorization: "Bearer " + token },
      body: deployment(started.jwt),
    });
    assert.equal(incomplete.status, 401);
    const completed = (await api(scriptPath + "/assets-upload-session", "POST", { manifest: {} }))
      .result;
    const other = "cf-cap-" + randomUUID().slice(0, 8);
    await api(cf + "/workers/workers", "POST", { name: other });
    const crossed = await fetch(origin + cf + "/workers/scripts/" + other, {
      method: "PUT",
      headers: { authorization: "Bearer " + token },
      body: deployment(completed.jwt),
    });
    assert.equal(crossed.status, 403);
    await api(cf + "/workers/scripts/" + other, "DELETE");
  },
);
await check(
  "official Wrangler uploads JSON, version metadata and assets without deploying",
  async () => {
    const assets = path.join(directory, "public");
    mkdirSync(assets, { recursive: true });
    writeFileSync(path.join(assets, "index.html"), "<h1>assets-one</h1>");
    writeFileSync(path.join(assets, "readme.txt"), "binding-file-one");
    writeFileSync(path.join(assets, "empty.txt"), "");
    bindingsConfig = {
      name: script,
      main: "index.js",
      compatibility_date: "2026-09-28",
      workers_dev: true,
      preview_urls: true,
      vars: {
        CONFIG: { nested: { enabled: true, nullable: null }, items: [1, "two", null] },
        COUNT: 42,
        FLAG: false,
        EMPTY: null,
      },
      version_metadata: { binding: "META" },
      assets: { directory: "public", binding: "ASSETS", run_worker_first: ["/api/*", "/bound"] },
      previews: { vars: { CONFIG: { preview: true } }, version_metadata: { binding: "META" } },
    };
    writeFileSync(config, JSON.stringify(bindingsConfig));
    writeFileSync(
      source,
      `export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === "/bound")
      return env.ASSETS.fetch(new Request("https://assets.invalid/readme.txt"));
    return Response.json({
      config: env.CONFIG,
      count: env.COUNT,
      flag: env.FLAG,
      empty: env.EMPTY,
      meta: env.META,
    });
  },
};
`,
    );
    await wrangler([
      "versions",
      "upload",
      "--tag",
      "assets-one",
      "--message",
      "JSON and asset fixture",
    ]);
    const version = (await api(scriptPath + "/versions")).result.items[0];
    bindingsVersion = version.id;
    assert.equal(version.annotations["workers/tag"], "assets-one");
    assert.equal(version.annotations["workers/message"], "JSON and asset fixture");
    assert(version.resources.bindings.some((b) => b.name === "CONFIG" && b.type === "json"));
    assert(
      version.resources.bindings.some((b) => b.name === "META" && b.type === "version_metadata"),
    );
    assert(version.resources.bindings.some((b) => b.name === "ASSETS" && b.type === "assets"));
    await invoke("production", { marker: "second" });
  },
);
await check("deployed JSON values and version metadata retain their actual types", async () => {
  const output = await wrangler(["versions", "deploy", bindingsVersion + "@100%", "--yes"]);
  assert(output.includes("assets-one"));
  let value;
  for (let i = 0; i < 60; i++) {
    const r = await invokePath("/api/config");
    try {
      value = JSON.parse(r.body);
    } catch {}
    if (value?.meta?.id === bindingsVersion) {
      break;
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  assert.deepEqual(value.config, {
    nested: { enabled: true, nullable: null },
    items: [1, "two", null],
  });
  assert.equal(value.count, 42);
  assert.equal(value.flag, false);
  assert.equal(value.empty, null);
  assert.equal(value.meta.tag, "assets-one");
  assert.equal(value.meta.id, bindingsVersion);
  assert(Number.isFinite(Date.parse(value.meta.timestamp)));
  const settings = (await api(scriptPath + "/settings")).result;
  assert.deepEqual(settings.bindings.find((b) => b.name === "CONFIG").json, value.config);
});
await check("official asset uploads serve static files and the ASSETS fetch binding", async () => {
  const root = await invokePath("/");
  assert.equal(root.status, 200);
  assert(root.body.includes("assets-one"));
  const bound = await invokePath("/bound");
  assert.equal(bound.status, 200);
  assert.equal(bound.body, "binding-file-one");
  const empty = await invokePath("/empty.txt");
  assert.equal(empty.status, 200);
  assert.equal(empty.body, "");
});
await check(
  "changing assets creates another immutable version and rollback restores old files",
  async () => {
    writeFileSync(path.join(directory, "public", "index.html"), "<h1>assets-two</h1>");
    writeFileSync(path.join(directory, "public", "readme.txt"), "binding-file-two");
    bindingsConfig.vars.CONFIG = { changed: true };
    writeFileSync(config, JSON.stringify(bindingsConfig));
    await wrangler(["versions", "upload", "--tag", "assets-two"]);
    assert((await invokePath("/")).body.includes("assets-one"));
    const next = (await api(scriptPath + "/versions")).result.items[0].id;
    await wrangler(["versions", "deploy", next + "@100%", "--yes"]);
    await assertEventually(async () => assert((await invokePath("/")).body.includes("assets-two")));
    const output = await wrangler(["versions", "deploy", bindingsVersion + "@100%", "--yes"]);
    assert(output.includes("assets-one"));
    await assertEventually(async () => assert((await invokePath("/")).body.includes("assets-one")));
    assert.equal((await invokePath("/bound")).body, "binding-file-one");
    const value = JSON.parse((await invokePath("/api/config")).body);
    assert.equal(value.meta.tag, "assets-one");
    assert.equal(value.config.nested.enabled, true);
  },
);
async function assertEventually(fn) {
  let error;
  for (let i = 0; i < 60; i++) {
    try {
      await fn();
      return;
    } catch (e) {
      error = e;
      await new Promise((r) => setTimeout(r, 500));
    }
  }
  throw error;
}
await check(
  "keep_assets reuses the deployed immutable files during a code-only upload",
  async () => {
    const form = new FormData();
    form.append(
      "metadata",
      JSON.stringify({
        main_module: "index.js",
        compatibility_date: "2026-09-28",
        keep_assets: true,
        bindings: [
          { name: "ASSETS", type: "assets" },
          { name: "META", type: "version_metadata" },
        ],
        annotations: { "workers/tag": "kept-assets" },
      }),
    );
    form.append(
      "index.js",
      new Blob(
        [
          `export default {
  fetch(request, env) {
    if (new URL(request.url).pathname === "/bound")
      return env.ASSETS.fetch(new Request("https://assets.invalid/readme.txt"));
    return Response.json({ meta: env.META });
  },
};
`,
        ],
        { type: "application/javascript+module" },
      ),
      "index.js",
    );
    const response = await fetch(origin + scriptPath, {
      method: "PUT",
      headers: { authorization: "Bearer " + token },
      body: form,
    });
    const body = await response.json();
    assert.equal(response.status, 200, JSON.stringify(body));
    await assertEventually(async () => assert((await invokePath("/")).body.includes("assets-one")));
    assert.equal((await invokePath("/bound")).body, "binding-file-one");
    await assertEventually(async () =>
      assert.equal(JSON.parse((await invokePath("/api/metadata")).body).meta.tag, "kept-assets"),
    );
  },
);
await check(
  "official Preview assets and JSON bindings remain isolated from production",
  async () => {
    await wrangler(["preview", "--name", "assets-preview", "--tag", "preview-assets"]);
    await assertEventually(async () =>
      assert((await invokePath("/", "assets-preview")).body.includes("assets-two")),
    );
    assert((await invokePath("/")).body.includes("assets-one"));
    const value = JSON.parse((await invokePath("/api/config", "assets-preview")).body);
    assert.deepEqual(value.config, { preview: true });
    assert(value.meta && value.meta.id !== bindingsVersion);
    assert.equal(value.meta.tag, "preview-assets");
    const details = (await api(workerPath + "/previews/assets-preview/deployments/latest")).result;
    assert.equal(details.annotations["workers/tag"], "preview-assets");
    assert.equal((await invokePath("/bound", "assets-preview")).body, "binding-file-two");
    await wrangler(["preview", "delete", "--name", "assets-preview", "--skip-confirmation"]);
  },
);
await check("official Wrangler deploy supports an assets-only Worker", async () => {
  const other = "cf-assets-" + randomUUID().slice(0, 8);
  writeFileSync(
    config,
    JSON.stringify({
      name: other,
      compatibility_date: "2026-09-28",
      assets: { directory: "public" },
    }),
  );
  await wrangler(["deploy"]);
  const details = (await api(cf + "/workers/workers/" + other)).result;
  const base = "/api/v1/orgs/" + org.id + "/workers/" + details.tag;
  await assertEventually(async () => {
    const r = await api(base + "/environments/production/invoke", "POST", {
      path: "/",
      method: "GET",
    });
    assert.equal(r.status, 200);
    assert(r.body.includes("assets-two"));
  });
  await api(cf + "/workers/scripts/" + other, "DELETE");
  writeFileSync(
    config,
    JSON.stringify({
      name: script,
      main: "index.js",
      compatibility_date: "2026-09-28",
      workers_dev: true,
      preview_urls: true,
    }),
  );
  code("second");
});
// Routing policies must survive official uploads, the serving runtime and rollback.
await check(
  "official --keep-vars retains omitted strings and JSON while incoming values win",
  async () => {
    const cfg = {
      name: script,
      main: "index.js",
      compatibility_date: "2026-09-28",
      vars: { TEXT: "retained", JSON: { nested: [1, true, null] }, NULL: null, CHANGED: "old" },
    };
    writeFileSync(source, `export default {fetch(r,env){return Response.json(env)}}`);
    writeFileSync(config, JSON.stringify(cfg));
    await wrangler(["deploy"]);
    cfg.vars = { CHANGED: "new" };
    writeFileSync(config, JSON.stringify(cfg));
    await wrangler(["deploy", "--keep-vars"]);
    await assertEventually(async () => {
      const r = JSON.parse((await invokePath("/api/vars")).body);
      assert.equal(r.TEXT, "retained");
      assert.deepEqual(r.JSON, { nested: [1, true, null] });
      assert.equal(r.NULL, null);
      assert.equal(r.CHANGED, "new");
    });
    await wrangler(["deploy"]);
    await assertEventually(async () => {
      const r = JSON.parse((await invokePath("/api/vars")).body);
      assert.equal(r.TEXT, undefined);
      assert.equal(r.JSON, undefined);
      assert.equal(r.CHANGED, "new");
    });
  },
);
let rulesConfig;
let rulesVersion;
await check("official rule files and base_path deploy as immutable asset policy", async () => {
  assert(
    process.env.KRUN_CF_E2E_GATEWAY_ORIGIN,
    "Expose the private test gateway to verify raw response headers",
  );
  const dir = path.join(directory, "public");
  mkdirSync(path.join(dir, "folder"), { recursive: true });
  for (const [file, text] of Object.entries({
    "file.html": "canonical-file",
    "folder/index.html": "canonical-folder",
    "404.html": "root-not-found",
    "folder/404.html": "folder-not-found",
    _headers:
      "/*\n X-Shared: common\n/blog/*\n X-Shared: assets\n X-Path: :splat\n ! X-Content-Type-Options",
    _redirects:
      "/blog/old/* /blog/:splat 301\n/blog/item/:id /blog/file?item=:id 302\n/blog/rewrite /blog/file 200\n/blog/file /blog/readme.txt 200",
  })) {
    writeFileSync(path.join(dir, file), text);
  }
  rulesConfig = {
    name: script,
    main: "index.js",
    compatibility_date: "2026-09-28",
    assets: {
      directory: "public",
      binding: "ASSETS",
      base_path: "/blog",
      html_handling: "auto-trailing-slash",
      not_found_handling: "404-page",
      run_worker_first: ["/api/*", "/bound"],
    },
  };
  writeFileSync(
    source,
    `export default {
  fetch(request, env) {
    const u = new URL(request.url);
    if (u.pathname === "/bound")
      return env.ASSETS.fetch(
        new Request("https://assets.invalid" + u.searchParams.get("path"), { redirect: "manual" }),
      );
    return new Response("worker-response");
  },
};
`,
  );
  writeFileSync(config, JSON.stringify(rulesConfig));
  await wrangler(["deploy"]);
  rulesVersion = (await api(scriptPath + "/versions")).result.items[0].id;
  await assertEventually(async () =>
    assert.equal((await invokePath("/blog/")).headers["x-shared"], "common, assets"),
  );
  const outside = await invokePath("/file");
  assert.equal(outside.body, "worker-response");
  assert.equal(outside.headers["x-shared"], undefined);
  const apiResponse = await invokePath("/api/anything");
  assert.equal(apiResponse.body, "worker-response");
  assert.equal(apiResponse.headers["x-shared"], undefined);
});
await check(
  "redirect splats, placeholders, query strings and one-pass internal rewrites work",
  async () => {
    let r = await invokePath("/blog/old/file?original=1");
    assert.equal(r.status, 301);
    assert.equal(r.headers.location, "/blog/file?original=1");
    assert.equal(r.headers["x-shared"], undefined);
    r = await invokePath("/blog/item/42");
    assert.equal(r.status, 302);
    assert.equal(r.headers.location, "/blog/file?item=42");
    r = await invokePath("/blog/rewrite");
    assert.equal(r.status, 200);
    assert.equal(r.body, "canonical-file");
    assert.equal(r.headers["x-path"], "rewrite");
    assert.equal(r.headers["x-content-type-options"], undefined);
  },
);
await check(
  "404-page finds the closest parent and private rule files are never served",
  async () => {
    let r = await invokePath("/blog/folder/deeper/no");
    assert.equal(r.status, 404);
    assert.equal(r.body, "folder-not-found");
    assert.equal(r.headers["x-shared"], "common, assets");
    r = await invokePath("/blog/no");
    assert.equal(r.status, 404);
    assert.equal(r.body, "root-not-found");
    for (const file of ["_headers", "_redirects"]) {
      r = await invokePath("/blog/" + file);
      assert.equal(r.status, 404);
      assert(!r.body.includes("X-Shared"));
    }
  },
);
await check("ASSETS binding uses the public mount and the same redirect/header rules", async () => {
  let r = await invokePath("/bound?path=" + encodeURIComponent("/blog/rewrite"));
  assert.equal(r.body, "canonical-file");
  assert.equal(r.headers["x-shared"], "common, assets");
  r = await invokePath("/bound?path=" + encodeURIComponent("/file"));
  assert.equal(r.status, 404);
  r = await invokePath("/bound?path=" + encodeURIComponent("/blog/old/file"));
  assert.equal(r.status, 301);
  assert.equal(r.headers.location, "/blog/file");
});
for (const mode of ["force-trailing-slash", "drop-trailing-slash"]) {
  await check("official " + mode + " canonicalizes files and directory indexes", async () => {
    rulesConfig.assets.html_handling = mode;
    writeFileSync(config, JSON.stringify(rulesConfig));
    await wrangler(["deploy"]);
    const slash = mode === "force-trailing-slash" ? "/" : "";
    await assertEventually(async () => {
      const r = await invokePath("/blog/folder.html?q=1");
      assert.equal(r.status, 307);
      assert.equal(r.headers.location, "/blog/folder" + slash + "?q=1");
    });
    let r = await invokePath("/blog/folder" + slash);
    assert.equal(r.body, "canonical-folder");
    r = await invokePath("/blog/file.html");
    assert.equal(r.status, 307);
    assert.equal(r.headers.location, "/blog/file" + slash);
    r = await invokePath("/blog/file" + slash);
    assert.equal(r.status, 200);
  });
}
await check("rollback restores asset rules, mount and HTML mode with its version", async () => {
  rulesConfig.assets.base_path = "/next";
  writeFileSync(config, JSON.stringify(rulesConfig));
  writeFileSync(path.join(directory, "public", "_headers"), "/*\n X-Shared: changed");
  writeFileSync(path.join(directory, "public", "_redirects"), "/next/rewrite /next/readme.txt 200");
  await wrangler(["deploy"]);
  await assertEventually(async () =>
    assert.equal((await invokePath("/next")).headers["x-shared"], "changed"),
  );
  assert.equal((await invokePath("/blog/rewrite")).body, "worker-response");
  await wrangler(["versions", "deploy", rulesVersion + "@100%", "--yes"]);
  await assertEventually(async () => {
    const r = await invokePath("/blog/folder");
    assert.equal(r.status, 307);
    assert.equal(r.headers.location, "/blog/folder/");
  });
  const restored = await invokePath("/blog/rewrite");
  assert.equal(restored.body, "canonical-file");
  assert.equal(restored.headers["x-shared"], "common, assets");
  assert.equal((await invokePath("/next")).body, "worker-response");
});
const deploymentPath = scriptPath + "/deployments";
let gradualGateway;
let gradualVersions;
let gradualAssets;
let gradualAtHalf;

function percentages(deployment) {
  assert.equal(deployment.strategy, "percentage");
  assert.equal(
    deployment.versions.length,
    new Set(deployment.versions.map((v) => v.version_id)).size,
  );
  return Object.fromEntries(deployment.versions.map((v) => [v.version_id, v.percentage]));
}
async function wranglerJSON(args) {
  return JSON.parse(await wrangler([...args, "--json"]));
}
async function activeDeployment(worker = native, endpoint = deploymentPath) {
  const result = (await api(endpoint)).result.deployments;
  assert(result.length > 0, "deployment history is empty");
  const latest = result[0];
  await assertEventually(async () => {
    const actual = await api(worker + "/deployments/" + latest.id);
    assert.equal(
      actual.state,
      "available",
      "deployment should converge: " + JSON.stringify(actual),
    );
  });
  return latest;
}
async function deployGradual(
  v1Weight,
  v2Weight,
  message,
  worker = native,
  endpoint = deploymentPath,
) {
  const [v1, v2] = gradualVersions;
  await wrangler([
    "versions",
    "deploy",
    v1 + "@" + v1Weight + "%",
    v2 + "@" + v2Weight + "%",
    "--yes",
    "--message",
    message,
  ]);
  const current = await activeDeployment(worker, endpoint);
  assert.deepEqual(percentages(current), { [v1]: v1Weight, [v2]: v2Weight });
  assert.equal(current.annotations["workers/message"], message);
  assert.deepEqual((await api(endpoint + "/" + current.id)).result, current);
  return current;
}
function fixtureAsset(marker) {
  const content = "console.log(" + JSON.stringify(marker) + ");";
  const hash = createHash("sha256").update(content).digest("hex").slice(0, 12);
  return { file: "app-" + hash + ".js", content };
}
function gradualFixture(workerName, marker) {
  const dir = path.join(directory, "gradual-assets");
  const asset = fixtureAsset(marker);
  rmSync(dir, { recursive: true, force: true });
  mkdirSync(path.join(dir, "assets"), { recursive: true });
  writeFileSync(path.join(dir, "index.html"), '<script src="/assets/' + asset.file + '"></script>');
  writeFileSync(path.join(dir, "assets", asset.file), asset.content);
  writeFileSync(
    config,
    JSON.stringify({
      name: workerName,
      main: "index.js",
      compatibility_date: "2026-09-28",
      vars: { LABEL: marker },
      version_metadata: { binding: "META" },
      assets: { directory: "gradual-assets", binding: "ASSETS" },
    }),
  );
  writeFileSync(
    source,
    `export default {
  fetch(request, env) {
    const cache = new URL(request.url).pathname === "/cache";
    return Response.json({
      code: ${JSON.stringify(marker)},
      label: env.LABEL,
      version: env.META.id,
      requestId: crypto.randomUUID(),
      appAuthorization: request.headers.get("authorization") === "Bearer fixture-app",
      verificationAuthorization: request.headers.has("krun-workers-verify-authorization"),
    }, { headers: { "Cache-Control": cache ? "public, s-maxage=60" : "no-store" } });
  },
};
`,
  );
  return asset;
}
async function gatewayVersion(gateway, headers = {}, url = "/version") {
  const response = await gatewayRequest(gateway, url, headers);
  assert.equal(response.status, 200, response.body);
  const value = JSON.parse(response.body);
  assert.equal(response.headers["x-krun-version"], value.version);
  assert.equal(value.code, value.label, "code and version-bound variables should agree");
  assert(response.headers["x-krun-pod"], "response should identify the serving Pod");
  return { ...value, pod: response.headers["x-krun-pod"], headers: response.headers };
}
async function uniformVersion(version) {
  const seen = new Set();
  for (let index = 0; index < 32; index++) {
    const response = await gatewayVersion(gradualGateway);
    assert.equal(response.version, version);
    seen.add(response.pod);
  }
  assert.equal(seen.size, 2, "expected both serving Pods");
  return [...seen].sort();
}
async function sampleGradualTraffic(expectedMinimum, expectedMaximum) {
  const [stable, candidate] = gradualVersions;
  let candidateCount = 0;
  const pods = new Set();
  for (let offset = 0; offset < 10000; offset += 32) {
    const batchSize = Math.min(32, 10000 - offset);
    const batch = await Promise.all(
      Array.from({ length: batchSize }, () => gatewayVersion(gradualGateway)),
    );
    for (const result of batch) {
      assert(result.version === stable || result.version === candidate, "unexpected version");
      assert.equal(result.code, result.version === stable ? "gradual-v1" : "gradual-v2");
      pods.add(result.pod);
      candidateCount += Number(result.version === candidate);
    }
  }
  assert.equal(pods.size, 2, "expected both serving Pods");
  assert(
    candidateCount >= expectedMinimum && candidateCount <= expectedMaximum,
    "candidate received " + candidateCount + " of 10,000 requests",
  );
  return candidateCount;
}
const cloudflareKey = "Cloudflare-Workers-Version-Key";
const krunKey = "Krun-Workers-Version-Key";
const overrideHeader = "Cloudflare-Workers-Version-Overrides";
function versionOverride(workerName, version) {
  return { [overrideHeader]: workerName + "=" + JSON.stringify(version) };
}
function bearer(key = token) {
  return "Bearer " + key;
}

await check("Wrangler stages a second version without changing production traffic", async () => {
  gradualGateway = await workerGateway(
    native,
    "production",
    "internal",
    process.env.KRUN_CF_E2E_GATEWAY_ORIGIN,
  );
  gradualAssets = [gradualFixture(script, "gradual-v1")];
  await wrangler(["deploy"]);
  const [old] = (await api(scriptPath + "/versions")).result.items;
  await activeDeployment();
  gradualAssets.push(gradualFixture(script, "gradual-v2"));
  await wrangler(["versions", "upload", "--tag", "gradual-v2"]);
  const [candidate] = (await api(scriptPath + "/versions")).result.items;
  gradualVersions = [old.id, candidate.id];
  assert.notEqual(candidate.id, old.id);
  const list = await wranglerJSON(["versions", "list"]);
  assert(list.some((v) => v.id === old.id));
  assert(list.some((v) => v.id === candidate.id));
  const view = await wranglerJSON(["versions", "view", candidate.id]);
  assert.equal(view.id, candidate.id);
  assert.equal(view.annotations["workers/tag"], "gradual-v2");
  await deployGradual(100, 0, "Stage candidate");
  await uniformVersion(old.id);
  const candidateAsset = await gatewayRequest(gradualGateway, "/assets/" + gradualAssets[1].file);
  assert(!candidateAsset.body.includes(gradualAssets[1].content));
});
await check("only authorized production overrides reach a zero-weight candidate", async () => {
  const [stable, candidate] = gradualVersions;
  const override = versionOverride(script, candidate);
  const unauthenticated = await gatewayRequest(gradualGateway, "/version", override);
  assert.equal(unauthenticated.status, 403);
  const reader = await api(
    orgNative + "/api-keys",
    "POST",
    {
      name: "Gradual read only",
      expires_in: 3600,
      policy: {
        schema_version: 1,
        statements: [
          {
            effect: "allow",
            actions: ["worker.read", "worker.version.read"],
            resource: { kind: "worker", organization_id: org.id, ids: [nativeWorker.id] },
          },
        ],
      },
    },
    true,
    201,
  );
  const denied = await gatewayRequest(gradualGateway, "/version", {
    ...override,
    Authorization: bearer(reader.key),
  });
  assert.equal(denied.status, 403);
  const noPublish = await fetch(origin + deploymentPath, {
    method: "POST",
    headers: { authorization: bearer(reader.key), "content-type": "application/json" },
    body: JSON.stringify({
      strategy: "percentage",
      versions: [
        { version_id: stable, percentage: 95 },
        { version_id: candidate, percentage: 5 },
      ],
    }),
  });
  assert.equal(noPublish.status, 403);
  const selected = await gatewayVersion(gradualGateway, { ...override, Authorization: bearer() });
  assert.equal(selected.version, candidate);
  assert.match(selected.headers["cache-control"], /no-store/);
  const dedicated = await gatewayVersion(gradualGateway, {
    ...override,
    Authorization: "Bearer fixture-app",
    "Krun-Workers-Verify-Authorization": bearer(),
  });
  assert.equal(dedicated.version, candidate);
  assert.equal(dedicated.appAuthorization, true);
  assert.equal(dedicated.verificationAuthorization, false);
  const unknown = await gatewayVersion(gradualGateway, {
    ...versionOverride(script, randomUUID()),
    Authorization: bearer(),
  });
  assert.equal(unknown.version, stable);
  const asset = await gatewayRequest(gradualGateway, "/assets/" + gradualAssets[1].file, {
    ...override,
    Authorization: bearer(),
  });
  assert.equal(asset.body, gradualAssets[1].content);
  assert.match(asset.headers["cache-control"], /no-store/);
});
await check("Wrangler sets 5% traffic and lists matching deployment history", async () => {
  const gradualAtFive = await deployGradual(95, 5, "Canary 5 percent");
  const status = await wranglerJSON(["deployments", "status"]);
  assert.equal(status.id, gradualAtFive.id);
  assert.deepEqual(percentages(status), { [gradualVersions[0]]: 95, [gradualVersions[1]]: 5 });
  const list = await wranglerJSON(["deployments", "list"]);
  assert(list.some((d) => d.id === status.id));
  assert(list.some((d) => d.annotations["workers/message"] === "Stage candidate"));
  console.log("5% candidate requests: " + (await sampleGradualTraffic(400, 600)));
});
const rolloutKeys = Array.from({ length: 256 }, (_, index) => "gradual-session-" + index);
async function readAssignments() {
  const values = [];
  for (const key of rolloutKeys) {
    values.push((await gatewayVersion(gradualGateway, { [cloudflareKey]: key })).version);
  }
  return values;
}
let atFive;
await check("both version keys stay stable across Pods at 5%", async () => {
  atFive = await readAssignments();
  const [stable, candidate] = gradualVersions;
  assert(atFive.includes(stable));
  assert(atFive.includes(candidate));
  const pods = new Set();
  for (const [index, key] of rolloutKeys.entries()) {
    const r = await gatewayVersion(gradualGateway, { [krunKey]: key });
    assert.equal(r.version, atFive[index]);
    pods.add(r.pod);
  }
  const key = rolloutKeys[atFive.indexOf(candidate)];
  const conflicting = rolloutKeys[atFive.indexOf(stable)];
  for (let i = 0; i < 32; i++) {
    const response = await gatewayVersion(gradualGateway, {
      [krunKey]: key,
      [cloudflareKey]: conflicting,
    });
    assert.equal(response.version, candidate);
    pods.add(response.pod);
  }
  assert.equal(pods.size, 2);
});
await check("increasing to 50% keeps existing candidate assignments", async () => {
  gradualAtHalf = await deployGradual(50, 50, "Canary 50 percent");
  const atHalf = await readAssignments();
  const candidate = gradualVersions[1];
  for (const [index, assignment] of atFive.entries()) {
    if (assignment === candidate) {
      assert.equal(atHalf[index], candidate, "moving the threshold should keep this session");
    }
  }
  console.log("50% candidate requests: " + (await sampleGradualTraffic(4800, 5200)));
});
await check(
  "invalid deployment lists are rejected without replacing the active rollout",
  async () => {
    const [stable, candidate] = gradualVersions;
    for (const versions of [
      [
        { version_id: stable, percentage: 80 },
        { version_id: candidate, percentage: 10 },
      ],
      [
        { version_id: stable, percentage: 50 },
        { version_id: stable, percentage: 50 },
      ],
      [
        { version_id: stable, percentage: 20 },
        { version_id: candidate, percentage: 30 },
        { version_id: firstVersion, percentage: 50 },
      ],
    ]) {
      const outcome = await api(
        deploymentPath,
        "POST",
        { strategy: "percentage", versions },
        false,
        422,
      );
      assert.equal(outcome.success, false);
      assert.equal(typeof outcome.errors[0].code, "number");
    }
    assert.equal((await api(deploymentPath)).result.deployments[0].id, gradualAtHalf.id);
  },
);
await check("Wrangler moves 100% to the candidate, promotes it and rolls back", async () => {
  const [stable, candidate] = gradualVersions;
  await deployGradual(99.875, 0.125, "Fractional weight");
  await deployGradual(0, 100, "Candidate takes full traffic");
  const pods = await uniformVersion(candidate);
  await wrangler(["versions", "deploy", candidate + "@100%", "--yes"]);
  const promoted = await activeDeployment();
  assert.deepEqual(percentages(promoted), { [candidate]: 100 });
  assert.deepEqual(await uniformVersion(candidate), pods);
  assert.equal(
    (await workerGateway(native, "production", "internal", gradualGateway.origin)).host,
    gradualGateway.host,
  );
  const oldAsset = await gatewayRequest(gradualGateway, "/assets/" + gradualAssets[0].file);
  assert.equal(oldAsset.status, 200);
  assert.equal(oldAsset.body, gradualAssets[0].content);

  await wrangler(["rollback", stable, "--message", "Restore gradual v1", "--yes"]);
  const restored = await activeDeployment();
  assert.deepEqual(percentages(restored), { [stable]: 100 });
  assert.equal(restored.annotations["workers/message"], "Restore gradual v1");
  assert.deepEqual(await uniformVersion(stable), pods);
  const newAsset = await gatewayRequest(gradualGateway, "/assets/" + gradualAssets[1].file);
  assert.equal(newAsset.status, 200);
  assert.equal(newAsset.body, gradualAssets[1].content);
});

await check("native retention override disables fallback for newly retired assets", async () => {
  const environment = async () =>
    (await api(native + "/environments")).items.find((item) => item.name === "production");
  const before = await environment();
  const updated = await api(native + "/environments/production", "PATCH", {
    expected_revision: before.configuration_revision,
    gradual_asset_retention_seconds: 0,
  });
  assert.equal(updated.gradual_asset_retention_seconds, 0);
  const configured = await environment();
  assert.equal(configured.gradual_asset_retention_seconds, 0);
  assert.equal(configured.configuration_revision, before.configuration_revision + 1);

  const asset = gradualFixture(script, "retention-disabled");
  await wrangler(["versions", "upload"]);
  const [temporary] = (await api(scriptPath + "/versions")).result.items;
  await wrangler(["versions", "deploy", temporary.id + "@100%", "--yes"]);
  await activeDeployment();
  const url = "/assets/" + asset.file;
  const present = await gatewayRequest(gradualGateway, url);
  assert.equal(present.status, 200);
  assert.equal(present.body, asset.content);
  assert(present.headers.etag, "active asset should have a validator");

  const [stable] = gradualVersions;
  await wrangler(["rollback", stable, "--yes"]);
  await activeDeployment();
  const retired = await gatewayRequest(gradualGateway, url, {
    "If-None-Match": present.headers.etag,
  });
  assert.notEqual(retired.status, 304);
  assert.notEqual(retired.body, asset.content);
  assert.equal(retired.headers["x-krun-asset-version"], undefined);
});

await check(
  "public responses cache by version while authorization bypasses the cache",
  async () => {
    const publicOrigin = process.env.KRUN_CF_E2E_PUBLIC_GATEWAY_ORIGIN;
    assert(publicOrigin, "Expose workers-public Gateway for response-cache verification");
    const name = "cf-cache-" + locatorSuffix;
    const worker = orgNative + "/workers/" + name;
    const endpoint = cf + "/workers/scripts/" + name;
    const resource = await api(orgNative + "/workers", "POST", { name }, false, 201);
    assert.equal(resource.locator, name);
    await api(worker + "/network-profile", "PUT", {
      network_profile: "external-service",
      expected_revision: 0,
    });
    try {
      const gateway = await workerGateway(worker, "production", "external", publicOrigin);
      gradualFixture(name, "cache-v1");
      await wrangler(["deploy"]);
      const [v1] = (await api(endpoint + "/versions")).result.items;
      await activeDeployment(worker, endpoint + "/deployments");
      gradualFixture(name, "cache-v2");
      await wrangler(["versions", "upload"]);
      const [v2] = (await api(endpoint + "/versions")).result.items;
      await wrangler(["versions", "deploy", v1.id + "@50%", v2.id + "@50%", "--yes"]);
      await activeDeployment(worker, endpoint + "/deployments");

      const crossWorker = await api(
        deploymentPath,
        "POST",
        {
          strategy: "percentage",
          versions: [
            { version_id: gradualVersions[0], percentage: 50 },
            { version_id: v1.id, percentage: 50 },
          ],
        },
        false,
        404,
      );
      assert.equal(crossWorker.success, false);

      const keys = new Map();
      for (const key of rolloutKeys.slice(0, 32)) {
        const selected = await gatewayVersion(gateway, { [krunKey]: key });
        keys.set(selected.version, key);
        if (keys.size === 2) {
          break;
        }
      }
      assert(keys.has(v1.id) && keys.has(v2.id));

      const warmed = new Map();
      for (const [version, key] of keys) {
        const ids = new Map();
        for (let i = 0; i < 16; i++) {
          const response = await gatewayVersion(gateway, { [krunKey]: key }, "/cache");
          assert.equal(response.version, version);
          assert.equal(response.code, version === v1.id ? "cache-v1" : "cache-v2");
          const previous = ids.get(response.pod);
          if (previous) {
            assert.equal(
              response.requestId,
              previous,
              "public cache should reuse the same response",
            );
          }
          ids.set(response.pod, response.requestId);
        }
        assert.equal(ids.size, 2);
        warmed.set(version, ids);
      }

      const candidateKey = keys.get(v2.id);
      const authenticated = await gatewayVersion(
        gateway,
        { [krunKey]: candidateKey, Authorization: "Bearer fixture-app" },
        "/cache",
      );
      assert.equal(authenticated.version, v2.id);
      assert.equal(authenticated.appAuthorization, true);
      assert.match(authenticated.headers["cache-control"], /no-store/);
      assert.notEqual(authenticated.requestId, warmed.get(v2.id).get(authenticated.pod));

      const verified = await gatewayVersion(
        gateway,
        {
          [krunKey]: keys.get(v1.id),
          ...versionOverride(name, v2.id),
          "Krun-Workers-Verify-Authorization": bearer(),
        },
        "/cache",
      );
      assert.equal(verified.version, v2.id);
      assert.equal(verified.verificationAuthorization, false);
      assert.match(verified.headers["cache-control"], /no-store/);
      assert.notEqual(verified.requestId, warmed.get(v2.id).get(verified.pod));
      assert.notEqual(verified.requestId, authenticated.requestId);

      const ordinary = await gatewayVersion(gateway, { [krunKey]: candidateKey }, "/cache");
      assert.equal(ordinary.version, v2.id);
      assert.equal(ordinary.appAuthorization, false);
      assert.equal(ordinary.requestId, warmed.get(v2.id).get(ordinary.pod));
    } finally {
      await api(endpoint, "DELETE");
    }
  },
);

await check(
  "Wrangler service bindings stream HTTP across pools and follow the target rollout",
  async () => {
    const targetName = "cf-isolated-" + locatorSuffix;
    const callerName = "cf-caller-" + locatorSuffix;
    const targetWorker = orgNative + "/workers/" + targetName;
    const callerWorker = orgNative + "/workers/" + callerName;
    const targetScript = cf + "/workers/scripts/" + targetName;
    const callerScript = cf + "/workers/scripts/" + callerName;
    const targetResource = await api(
      orgNative + "/workers",
      "POST",
      { name: targetName },
      false,
      201,
    );
    await api(targetWorker + "/network-profile", "PUT", {
      network_profile: "isolated-service",
      expected_revision: 0,
    });

    function targetFixture(label) {
      writeFileSync(
        config,
        JSON.stringify({
          name: targetName,
          main: "index.js",
          compatibility_date: "2026-09-28",
          vars: { LABEL: label },
          version_metadata: { binding: "META" },
        }),
      );
      writeFileSync(
        source,
        `export default {
  async fetch(request, env) {
    return Response.json({
      label: env.LABEL,
      version: env.META.id,
      method: request.method,
      url: request.url,
      body: await request.text(),
      appAuth: request.headers.get("authorization"),
      managedHost: request.headers.get("x-krun-service-host"),
    }, { status: 202 });
  },
};
`,
      );
    }

    async function callerBinding(gateway, path, key, body = null) {
      const headers = { [krunKey]: key, authorization: "Bearer fixture-app" };
      return gatewayRequest(gateway, path, headers, body === null ? "GET" : "PATCH", body);
    }

    let targetDeleted = false;
    let bindingGrant;
    try {
      targetFixture("isolated-v1");
      await wrangler(["deploy"]);
      const [v1] = (await api(targetScript + "/versions")).result.items;
      await activeDeployment(targetWorker, targetScript + "/deployments");
      targetFixture("isolated-v2");
      await wrangler(["versions", "upload"]);
      const [v2] = (await api(targetScript + "/versions")).result.items;
      await wrangler(["versions", "deploy", v1.id + "@50%", v2.id + "@50%", "--yes"]);
      await activeDeployment(targetWorker, targetScript + "/deployments");

      const callerConfig = {
        name: callerName,
        main: "index.js",
        compatibility_date: "2026-09-28",
        services: [
          { binding: "LOCAL", service: script },
          { binding: "REMOTE", service: targetName },
        ],
      };
      writeFileSync(config, JSON.stringify(callerConfig));
      writeFileSync(
        source,
        `export default {
  fetch(request, env) {
    const url = new URL(request.url);
    const destination = "https://app.example/target?q=a%2Fb";
    const key = url.searchParams.get("key");
    const forwarded = new Request(destination, request);
    if (key !== null) {
      forwarded.headers.set("Cloudflare-Workers-Version-Key", key);
    }
    const bindings = {
      "/local": env.LOCAL,
      "/fixed": env.FIXED,
      "/fixed-local": env.FIXED_LOCAL,
    };
    const binding = bindings[url.pathname] ?? env.REMOTE;
    return binding.fetch(forwarded);
  },
};
`,
      );
      const callerResource = await api(
        orgNative + "/workers",
        "POST",
        { name: callerName },
        false,
        201,
      );
      await wrangler(["deploy"]);
      await activeDeployment(callerWorker, callerScript + "/deployments");
      const callerVersion = (await api(callerScript + "/versions")).result.items[0].id;
      const gateway = await workerGateway(
        callerWorker,
        "production",
        "internal",
        gradualGateway.origin,
      );
      const target = await workerGateway(
        targetWorker,
        "production",
        "isolated",
        gradualGateway.origin,
      );

      const samePool = await callerBinding(gateway, "/local", "service-local");
      assert.equal(samePool.status, 200, samePool.body);
      assert.equal(JSON.parse(samePool.body).version, gradualVersions[0]);

      const observed = new Set();
      for (const key of rolloutKeys.slice(0, 16)) {
        const direct = await gatewayRequest(target, "/target", { [krunKey]: key });
        assert.equal(direct.status, 202);
        const selected = JSON.parse(direct.body);
        const forwarded = await callerBinding(gateway, "/remote?key=" + key, key, "request body");
        assert.equal(forwarded.status, 202, forwarded.body);
        const actual = JSON.parse(forwarded.body);
        assert.equal(actual.version, selected.version);
        assert.equal(actual.label, selected.label);
        assert.equal(actual.url, "https://app.example/target?q=a%2Fb");
        assert.equal(actual.method, "PATCH");
        assert.equal(actual.body, "request body");
        assert.equal(actual.appAuth, "Bearer fixture-app");
        assert.equal(actual.managedHost, null);
        observed.add(actual.version);
      }
      assert(observed.has(v1.id) && observed.has(v2.id));

      const callerEnvironment = (await api(callerWorker + "/environments")).items.find(
        (environment) => environment.name === "production",
      );
      await api(callerWorker + "/environments/production", "PATCH", {
        expected_revision: callerEnvironment.configuration_revision,
        service_bindings: {
          LOCAL: { service: script },
          REMOTE: { service: targetName },
          FIXED: { service: targetName, version_id: v1.id },
          FIXED_LOCAL: { service: script, version_id: gradualVersions[1] },
        },
      });
      bindingGrant = await api(
        orgNative + "/api-keys",
        "POST",
        {
          name: "Delegated service binding",
          expires_in: 3600,
          policy: {
            schema_version: 1,
            statements: [
              {
                effect: "allow",
                actions: ["worker.read", "worker.publish"],
                resource: { kind: "worker", organization_id: org.id, ids: [callerResource.id] },
              },
              {
                effect: "allow",
                actions: ["worker.invoke"],
                resource: {
                  kind: "worker",
                  organization_id: org.id,
                  ids: [targetResource.id, nativeWorker.id],
                },
              },
            ],
          },
        },
        true,
        201,
      );
      const form = new FormData();
      form.append(
        "metadata",
        JSON.stringify({
          main_module: "index.js",
          compatibility_date: "2026-09-28",
          bindings: ["LOCAL", "REMOTE", "FIXED", "FIXED_LOCAL"].map((name) => ({
            name,
            type: "inherit",
          })),
        }),
      );
      form.append(
        "index.js",
        new Blob([readFileSync(source)], { type: "application/javascript+module" }),
        "index.js",
      );
      const fixedCaller = await fetch(origin + callerScript, {
        method: "PUT",
        headers: { authorization: bearer(bindingGrant.key) },
        body: form,
        signal: AbortSignal.timeout(180000),
      });
      assert.equal(fixedCaller.status, 200, await fixedCaller.text());
      await activeDeployment(callerWorker, callerScript + "/deployments");
      targetFixture("isolated-v2");
      await wrangler(["versions", "deploy", v2.id + "@100%", "--yes"]);
      await activeDeployment(targetWorker, targetScript + "/deployments");
      const bindings = [
        { path: "/fixed", status: 202, version: v1.id },
        { path: "/remote", status: 202, version: v2.id },
        { path: "/fixed-local", status: 200, version: gradualVersions[1] },
        { path: "/local", status: 200, version: gradualVersions[0] },
      ];
      await Promise.all(
        [...bindings, ...bindings].map(async (expected, index) => {
          const response = await callerBinding(gateway, expected.path, "service-" + index);
          assert.equal(response.status, expected.status, response.body);
          assert.equal(JSON.parse(response.body).version, expected.version);
        }),
      );

      await api("/api/v1/api-keys/" + bindingGrant.id, "DELETE", undefined, true, 204);
      bindingGrant = undefined;
      const revokedBinding = await callerBinding(gateway, "/fixed", "service-fixed");
      assert.equal(revokedBinding.status, 403, "revoked grant remained callable");
      writeFileSync(config, JSON.stringify(callerConfig));
      await wrangler(["versions", "deploy", callerVersion + "@100%", "--yes"]);
      await activeDeployment(callerWorker, callerScript + "/deployments");
      assert.equal((await callerBinding(gateway, "/remote", "service-remote")).status, 202);

      await api(targetScript, "DELETE");
      targetDeleted = true;
      const revoked = await callerBinding(gateway, "/remote", "service-remote");
      assert([403, 404].includes(revoked.status), "deleted target remained callable");
    } finally {
      if (bindingGrant) {
        await api("/api/v1/api-keys/" + bindingGrant.id, "DELETE", undefined, true, 204);
      }
      await api(callerScript, "DELETE");
      if (!targetDeleted) {
        await api(targetScript, "DELETE");
      }
    }
  },
);

// Identity lifecycle checks use a plain Worker after the asset-policy fixtures.
writeFileSync(
  config,
  JSON.stringify({
    name: script,
    main: "index.js",
    compatibility_date: "2026-09-28",
    workers_dev: true,
    preview_urls: true,
  }),
);
await check("deleted Script names receive a fresh internal service identity", async () => {
  const before = (await api(workerPath)).result.tag;
  await api(scriptPath, "DELETE");
  await api(workerPath, "GET", undefined, false, 404);
  code("recreated");
  await wrangler(["deploy"]);
  const after = (await api(workerPath)).result.tag;
  assert.notEqual(before, after);
  const secrets = (await api(scriptPath + "/secrets")).result;
  assert.equal(secrets.length, 0);
});
await check("native service deletion cannot resurrect a previous Script identity", async () => {
  const id = (await api(workerPath)).result.tag;
  await api("/api/v1/orgs/" + org.id + "/workers/" + id, "DELETE", undefined, false, 204);
  await api(workerPath, "GET", undefined, false, 404);
  const response = await api(cf + "/workers/workers", "POST", { name: script });
  assert.notEqual(response.result.tag, id);
  await api("/api/v1/orgs/" + org.id + "/workers/" + id, "GET", undefined, false, 404);
});
gatewayAgent.destroy();
console.log("CF WRANGLER E2E PASSED " + results.length + " cases");
