import { test, expect, request as httpRequest } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import {
  mkdirSync,
  readFileSync,
  writeFileSync,
  readdirSync,
  existsSync,
  statSync,
} from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { randomUUID } from "node:crypto";

test.describe.configure({ mode: "serial" });
const pageErrors = new WeakMap();
test.beforeEach(async ({ page }) => {
  const errors = [];
  pageErrors.set(page, errors);
  page.on("pageerror", (error) => {
    if (new URL(page.url()).origin === origin) errors.push(error.message);
  });
});
test.afterEach(async ({ page }) => {
  expect(
    pageErrors.get(page),
    "authorization page JavaScript exceptions",
  ).toEqual([]);
});

const origin = process.env.KRUN_UI_ORIGIN || "http://127.0.0.1:4380";
const artifacts = path.resolve(".data/oauth/browser");
mkdirSync(artifacts, { recursive: true });
const binary = path.resolve(process.env.KRUN_CLI_BINARY || ".data/oauth/krun");
test.beforeAll(() => {
  if (!process.env.KRUN_CLI_BINARY) {
    execFileSync("go", ["build", "-trimpath", "-o", binary, "./cmd/krun"], {
      stdio: "pipe",
      timeout: 120000,
    });
  }
});
function processCommand(command, args, env = {}) {
  const child = spawn(command, args, {
    env: { ...process.env, KRUN_TOKEN: "", KRUN_SERVER: "", ...env },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stdout = "",
    stderr = "";
  child.stdout.on("data", (b) => (stdout += b));
  child.stderr.on("data", (b) => (stderr += b));
  const timeout = setTimeout(() => child.kill("SIGTERM"), 170000);
  const done = new Promise((resolve) =>
    child.on("exit", (code) => {
      clearTimeout(timeout);
      resolve({ code, stdout, stderr });
    }),
  );
  return { child, done, output: () => stdout + "\n" + stderr };
}
async function loginURL(command) {
  let found;
  await expect
    .poll(
      () => {
        found = command
          .output()
          .match(
            /(?:Open |Opening a link in your default browser: |Visit this link to authenticate: )(https?:\/\/[^\s\u001b]+)/,
          )?.[1];
        return Boolean(found);
      },
      { timeout: 15000 },
    )
    .toBeTruthy();
  return found;
}
async function consent(page, link, application) {
  await page.goto(link);
  const local = page.getByRole("button", {
    name: /通过 .*本地开发登录.* 登录|通过 .*kind.* 登录/,
  }).first();
  await expect(
    local.or(page.getByRole("heading", { name: `允许 ${application} 访问？` })),
  ).toBeVisible();
  if (await local.isVisible()) {
    await local.click();
  }
  await expect(
    page.getByRole("heading", { name: `允许 ${application} 访问？` }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "切换账号" })).toBeVisible();
}
function credential(raw, name) {
  return (
    raw.match(new RegExp("^" + name + ":\\s*[\"']?([^\\s\"']+)", "m"))?.[1] ||
    ""
  );
}
async function unauthenticated(token, expected) {
  const ctx = await httpRequest.newContext({
    baseURL: origin,
    extraHTTPHeaders: { authorization: "Bearer " + token },
  });
  const res = await ctx.get("/api/auth/me");
  expect(res.status()).toBe(expected);
  await ctx.dispose();
}
async function run(command, args, env = {}) {
  const proc = processCommand(command, args, env);
  const out = await proc.done;
  expect(out.code, out.stderr).toBe(0);
  return out;
}

test("krun browser login, automatic refresh and logout use the real issuer", async ({
  page,
}) => {
  const folder = path.join(artifacts, randomUUID());
  mkdirSync(folder);
  const config = path.join(folder, "profile.yaml");
  const proc = processCommand(binary, [
    "login",
    "--browser=false",
    "--callback-port",
    "0",
    "--server",
    origin,
    "--config",
    config,
  ]);
  try {
    await consent(page, await loginURL(proc), "krun CLI");
    await expect(
      page.getByText("访问你的工作空间", { exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: path.join(artifacts, "consent-krun-desktop.png"),
      fullPage: true,
    });
    await page.getByRole("button", { name: "允许连接", exact: true }).click();
    const result = await proc.done;
    expect(result.code, result.stderr).toBe(0);
    await expect(
      page.getByRole("heading", { name: "返回终端" }),
    ).toBeVisible();
    expect(statSync(config).mode & 0o777).toBe(0o600);
    const before = readFileSync(config, "utf8");
    const token = credential(before, "token");
    expect(token).toMatch(/^krun_cli_/);
    await unauthenticated(token, 200);
    writeFileSync(
      config,
      before.replace(/^expires_at:.*$/m, 'expires_at: "2000-01-01T00:00:00Z"'),
      { mode: 0o600 },
    );
    await run(binary, ["whoami", "--config", config]);
    const after = readFileSync(config, "utf8");
    expect(credential(after, "refresh_token")).not.toBe(
      credential(before, "refresh_token"),
    );
    const current = credential(after, "token");
    await run(binary, ["logout", "--config", config]);
    await unauthenticated(current, 401);
    await unauthenticated(token, 401);
  } finally {
    proc.child.kill();
  }
});
test("device authorization is clear on mobile and returns success without a callback", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const folder = path.join(artifacts, randomUUID());
  mkdirSync(folder);
  const config = path.join(folder, "profile.yaml");
  const proc = processCommand(binary, [
    "login",
    "--device",
    "--browser=false",
    "--server",
    origin,
    "--config",
    config,
  ]);
  try {
    await consent(page, await loginURL(proc), "krun CLI");
    const code = proc
      .output()
      .match(/Confirm code: ([A-F0-9]{7}-[A-F0-9]{7})/)?.[1];
    expect(code).toBeTruthy();
    await expect(page.getByText(code, { exact: true })).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: path.join(artifacts, "consent-device-mobile.png"),
      fullPage: true,
    });
    await page.getByRole("button", { name: "允许连接", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "连接已确认" }),
    ).toBeVisible();
    const result = await proc.done;
    expect(result.code, result.stderr).toBe(0);
    await run(binary, ["logout", "--config", config]);
  } finally {
    proc.child.kill();
  }
});
test("cancelling browser authorization reaches the CLI without issuing credentials", async ({
  page,
}) => {
  const folder = path.join(artifacts, randomUUID());
  mkdirSync(folder);
  const config = path.join(folder, "profile.yaml");
  const proc = processCommand(binary, [
    "login",
    "--browser=false",
    "--callback-port",
    "0",
    "--server",
    origin,
    "--config",
    config,
  ]);
  try {
    await consent(page, await loginURL(proc), "krun CLI");
    await page.getByRole("button", { name: "取消授权", exact: true }).click();
    const result = await proc.done;
    expect(result.code).not.toBe(0);
    expect(result.stderr).toContain("authorization denied");
    expect(existsSync(config)).toBeFalsy();
  } finally {
    proc.child.kill();
  }
});
test("expired authorization has a useful state and no approval action", async ({
  page,
}) => {
  await page.goto("/?oauth=nonexistent-authorization");
  await expect(
    page.getByRole("heading", { name: "此授权请求已失效" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "允许连接" })).toHaveCount(0);
  await page.screenshot({
    path: path.join(artifacts, "consent-expired.png"),
    fullPage: true,
  });
});
test("official Wrangler signs in through krun, refreshes, deploys and revokes access", async ({
  page,
}) => {
  expect(
    existsSync(path.join(homedir(), ".wrangler")),
    "Use an isolated environment when a legacy Wrangler config directory exists",
  ).toBeFalsy();
  const folder = path.join(artifacts, randomUUID());
  mkdirSync(folder);
  const xdg = path.join(folder, "config");
  mkdirSync(xdg);
  const cache = path.join(folder, "cache");
  mkdirSync(cache);
  const env = {
    ...process.env,
    XDG_CONFIG_HOME: xdg,
    XDG_CACHE_HOME: cache,
    WRANGLER_AUTH_DOMAIN: new URL(origin).host,
    WRANGLER_AUTH_URL: origin + "/api/auth/oauth/authorize",
    WRANGLER_TOKEN_URL: origin + "/api/auth/oauth/token",
    WRANGLER_REVOKE_URL: origin + "/api/auth/oauth/revoke",
    WRANGLER_CLIENT_ID: "krun-wrangler",
    CLOUDFLARE_API_BASE_URL: origin + "/api/v1/cloudflare/client/v4",
    WRANGLER_SEND_METRICS: "false",
    CLOUDFLARE_AUTH_USE_KEYRING: "false",
    WRANGLER_LOG_PATH: path.join(folder, "wrangler.log"),
    CI: "true",
  };
  for (const key of [
    "CLOUDFLARE_API_TOKEN",
    "CF_API_TOKEN",
    "CLOUDFLARE_API_KEY",
    "CF_API_KEY",
    "CLOUDFLARE_EMAIL",
    "CF_EMAIL",
    "CLOUDFLARE_ACCOUNT_ID",
    "CF_ACCOUNT_ID",
  ])
    delete env[key];
  // processCommand merges the parent's env. Clear credentials explicitly there.
  for (const key of [
    "CLOUDFLARE_API_TOKEN",
    "CF_API_TOKEN",
    "CLOUDFLARE_API_KEY",
    "CF_API_KEY",
    "CLOUDFLARE_EMAIL",
    "CF_EMAIL",
    "CLOUDFLARE_ACCOUNT_ID",
    "CF_ACCOUNT_ID",
  ])
    env[key] = "";
  const wrangler = path.resolve(
    "workers/builder/node_modules/wrangler/bin/wrangler.js",
  );
  const proc = processCommand(
    process.execPath,
    [wrangler, "login", "--browser=false"],
    env,
  );
  try {
    await consent(page, await loginURL(proc), "Wrangler");
    await expect(
      page.getByText("管理 Worker 与部署", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("访问你的工作空间", { exact: true }),
    ).toHaveCount(0);
    await page.screenshot({
      path: path.join(artifacts, "consent-wrangler-desktop.png"),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "允许连接", exact: true })
      .click({ noWaitAfter: true });
    const logged = await proc.done;
    expect(logged.code, logged.stderr).toBe(0);
    const authFiles = readdirSync(xdg, { recursive: true }).filter((f) =>
      f.endsWith("default.toml"),
    );
    expect(authFiles).toHaveLength(1);
    const authFile = path.join(xdg, authFiles[0]);
    const before = readFileSync(authFile, "utf8");
    const old = before.match(/oauth_token\s*=\s*"([^"]+)"/)?.[1];
    expect(old).toMatch(/^krun_cli_/);
    await run(process.execPath, [wrangler, "whoami"], env);
    const workspace = await page
      .context()
      .request.get(origin + "/api/v1/cloudflare/client/v4/accounts");
    expect(workspace.ok()).toBeTruthy();
    const org = (await workspace.json()).result[0];
    expect(org).toBeTruthy();
    env.CLOUDFLARE_ACCOUNT_ID = org.id;
    const project = path.join(folder, "worker");
    mkdirSync(project);
    const config = path.join(project, "wrangler.json");
    const name = "oauth-" + randomUUID().slice(0, 8);
    writeFileSync(
      config,
      JSON.stringify({
        name,
        main: "index.js",
        compatibility_date: "2026-09-28",
        workers_dev: true,
        preview_urls: true,
      }),
    );
    writeFileSync(
      path.join(project, "index.js"),
      'export default {fetch(){return new Response("oauth-deployment")}}',
    );
    expect(before).toMatch(/expiration_time\s*=/);
    writeFileSync(
      authFile,
      before.replace(
        /expiration_time\s*=\s*"[^"]+"/,
        'expiration_time = "2000-01-01T00:00:00Z"',
      ),
    );
    await run(process.execPath, [wrangler, "deploy", "--config", config], env);
    const after = readFileSync(authFile, "utf8");
    const current = after.match(/oauth_token\s*=\s*"([^"]+)"/)?.[1];
    expect(current).not.toBe(old);
    await unauthenticated(current, 200);
    await run(process.execPath, [wrangler, "logout"], env);
    await unauthenticated(current, 401);
    await unauthenticated(old, 401);
  } finally {
    proc.child.kill();
  }
});
