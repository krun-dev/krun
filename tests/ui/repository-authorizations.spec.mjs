import { test, expect } from "@playwright/test";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, resolve } from "node:path";

let server;
let origin;
const base = "/api/v1/orgs/org-repos";
const worker = base + "/workers/worker-repos";
const grant = base + "/worker-repositories/gitlab";

test.beforeAll(async () => {
  const root = resolve("dist/console");
  server = createServer(async (request, response) => {
    const path = new URL(request.url, "http://localhost").pathname;
    const file = resolve(root, path === "/" ? "index.html" : path.slice(1));
    if (!file.startsWith(root + "/")) {
      response.writeHead(404).end();
      return;
    }
    try {
      const body = await readFile(file);
      const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css" };
      response.writeHead(200, { "Content-Type": types[extname(file)] || "application/octet-stream" });
      response.end(body);
    } catch {
      response.writeHead(404).end();
    }
  });
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  origin = `http://127.0.0.1:${server.address().port}`;
});
test.afterAll(async () => {
  await new Promise((done) => server.close(done));
});

async function fixture(page, initialStatus, provider = "gitlab") {
  const github = provider === "github";
  const endpoint = github ? base + "/worker-repositories/github" : grant;
  const state = { status: initialStatus, revoked: false, lists: 0, binding: null };
  const environment = {
    name: "production",
    enabled: true,
    domains: [],
    hosts: [],
    configuration_revision: 1,
    deployment_revision: 0,
  };
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));
  await page.route("https://gitlab.example.com/oauth/authorize", async (route) => {
    state.status = "connected";
    state.revoked = false;
    await route.fulfill({
      status: 302,
      headers: {
        location: origin + "/?page=workers&workspace=org-repos&repository_status=connected&repository_provider=gitlab&repository_resume=fixture-resume",
      },
    });
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let status = 200;
    let json = { items: [] };
    if (path === "/api/auth/me") {
      json = { user: { user_id: "repo-user", name: "Repository tester" }, workers_configured: true, revision: 1 };
    } else if (path === "/api/auth/providers") {
      json = { providers: [], development: false };
    } else if (path === "/api/auth/csrf") {
      json = { csrf_token: "fixture" };
    } else if (path === "/api/v1/orgs") {
      json = { items: [{ id: "org-repos", name: "Repositories", revision: 1 }] };
    } else if (path.endsWith("/permissions")) {
      json = { organization_id: "org-repos", actions: { "organization.read": true, "worker.list": true }, revision: 1 };
    } else if (path === base + "/workers") {
      json = { items: [{ id: "worker-repos", name: "edge-api", environments: [environment] }] };
    } else if (path === worker + "/overview") {
      json = { builds: [], versions: {}, environments: [{ environment, history: [] }], operations: [] };
    } else if (path === worker + "/repository") {
      if (request.method() === "PUT") state.binding = request.postDataJSON();
      json = { source: state.binding?.source ?? null, revision: state.binding ? 2 : 1 };
    } else if (path === worker + "/domains") {
      json = { selected: [], catalog: [], revision: 1, network_profile: {} };
    } else if (path === base + "/workers/repository-providers") {
      json = {
        items: [{
          id: provider, name: github ? "GitHub" : "公司 GitLab", type: provider, oauth: true,
          base_url: github ? "https://github.com" : "https://gitlab.example.com",
          install_url: github ? "https://github.com/apps/example/installations/new" : undefined,
          connected: state.status === "connected",
          authorization_status: state.status,
        }],
      };
    } else if (path === endpoint + "/connect") {
      json = { authorization_url: "https://gitlab.example.com/oauth/authorize", resume_key: "fixture-resume" };
    } else if (path === endpoint + "/repositories") {
      state.lists++;
      if (state.revoked) {
        state.status = "reconnect_required";
        status = 409;
        json = { error: { code: "WORKER_REPOSITORY_RECONNECT_REQUIRED", message: "仓库授权已过期，请重新授权。" } };
      } else {
        json = { items: [{ name: "platform/edge-api", private: true }], has_more: false };
      }
    }
    await route.fulfill({ status, json });
  });
  return { state, errors };
}

test("repository authorization is checked on demand and can be restored from personal settings", async ({ page }) => {
  const { state, errors } = await fixture(page, "connected");
  await page.clock.install();
  await page.goto(origin + "/?workspace=org-repos&page=git-authorizations");
  await expect(page.getByRole("heading", { name: "Git 仓库授权" })).toBeVisible();
  await expect(page.getByText("已授权", { exact: true })).toBeVisible();
  await page.clock.fastForward(5 * 60_000);
  expect(state.lists).toBe(0);
  state.revoked = true;
  await page.getByRole("button", { name: "验证授权" }).click();
  await expect(page.getByText("需要重新授权", { exact: true })).toBeVisible();
  expect(state.lists).toBe(1);
  await page.getByRole("button", { name: "重新授权", exact: true }).click();
  await expect(page).toHaveURL(/page=git-authorizations/);
  await expect(page.getByText("已授权", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("repository OAuth returns to the original Worker and binds a selected repository", async ({ page }) => {
  const { state, errors } = await fixture(page, "not_connected");
  await page.goto(origin + "/?workspace=org-repos&page=workers&worker=worker-repos&worker_tab=source");
  await page.getByRole("combobox", { name: "仓库平台" }).selectOption("gitlab");
  await page.getByRole("button", { name: "连接账号", exact: true }).click();
  const picker = page.getByRole("dialog", { name: "选择已授权仓库" });
  await expect(picker).toBeVisible();
  await expect(page).toHaveURL(/worker=worker-repos/);
  await picker.getByRole("button", { name: /platform\/edge-api/ }).click();
  await expect(page.getByLabel("仓库地址")).toHaveValue("platform/edge-api");
  await page.getByRole("button", { name: "保存仓库配置", exact: true }).click();
  await expect.poll(() => state.binding).toMatchObject({
    source: { provider_id: "gitlab", repository: "platform/edge-api" },
    expected_revision: 1,
    use_authorization: true,
  });
  expect(errors).toEqual([]);
});

test("GitHub installation is only offered in repository picker help", async ({ page }) => {
  const { state, errors } = await fixture(page, "connected", "github");
  const install = page.getByRole("link", { name: "安装 GitHub App 或调整仓库范围" });

  await page.goto(origin + "/?workspace=org-repos&page=git-authorizations");
  await expect(page.getByRole("heading", { name: "GitHub" })).toBeVisible();
  await expect(page.getByRole("link", { name: /GitHub App/ })).toHaveCount(0);

  await page.goto(origin + "/?workspace=org-repos&page=workers&worker=worker-repos&worker_tab=source");
  await page.getByRole("combobox", { name: "仓库平台" }).selectOption("github");
  await expect(page.getByRole("link", { name: /GitHub App/ })).toHaveCount(0);
  await page.getByRole("button", { name: "选择已授权仓库" }).click();
  await expect(page.getByRole("button", { name: /platform\/edge-api/ })).toBeVisible();
  await expect(install).toBeHidden();
  await page.getByText("找不到仓库？", { exact: true }).click();
  await expect(install).toHaveAttribute("href", "https://github.com/apps/example/installations/new");
  await page.getByRole("button", { name: "刷新仓库列表" }).click();
  await expect(page.getByRole("button", { name: /platform\/edge-api/ })).toBeVisible();
  expect(state.lists).toBe(2);
  expect(errors).toEqual([]);
});

test("expired repository authorization explains how to reconnect", async ({ page }) => {
  await fixture(page, "not_connected");
  await page.goto(
    origin + "/?workspace=org-repos&page=git-authorizations&repository_status=error&error=WORKER_REPOSITORY_STATE_INVALID",
  );
  await expect(
    page.getByText("仓库授权请求已失效，请返回 Git 仓库授权页面重新连接，并在同一浏览器中完成授权。"),
  ).toBeVisible();
});
