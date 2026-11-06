import { test, expect } from "@playwright/test";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { resolve, extname } from "node:path";

// Exercise the built static SPA at one origin. API fixtures isolate navigation
// and request races; org_permissions_test.go covers real HTTP authorization.
let server;
let origin;
const browserErrors = new WeakMap();
test.beforeEach(async ({ page }) => {
  const errors = [];
  browserErrors.set(page, errors);
  page.on("pageerror", (error) => errors.push(error.message));
});
test.afterEach(async ({ page }) => {
  expect(browserErrors.get(page)).toEqual([]);
});
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
      const content = await readFile(file);
      const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css" };
      response.writeHead(200, {
        "Content-Type": types[extname(file)] || "application/octet-stream",
      });
      response.end(content);
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

const viewer = {
  "organization.read": true,
  "service.list": true,
  "model.list": true,
  "apiKey.create": true,
  "storage.object.read": true,
};
const admin = {
  ...viewer,
  "service.create": true,
  "member.read": true,
  "iam.read": true,
  "audit.read": true,
  "storage.object.read": true,
};
const organization = (id) => ({ id, name: id, allowed_sources: [], revision: 1 });

async function fixture(page, options = {}) {
  const state = {
    orgs: [organization("org-a")],
    actions: { "org-a": viewer },
    systemAdmin: false,
    selfService: true,
    permissionsStatus: 200,
    ...options,
  };
  const requests = [];
  await page.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    requests.push(path);
    let data;
    let status = 200;
    if (path === "/api/auth/providers") {
      data = { providers: [], development: false, organization_self_service: state.selfService };
    } else if (path === "/api/auth/me") {
      data = {
        user: { user_id: "user-test", name: "Test user", source_id: "test", status: "active" },
        system_admin: state.systemAdmin,
        credential_kind: "session",
        revision: 1,
        workers_configured: true,
      };
    } else if (path === "/api/v1/orgs") {
      data = { items: state.orgs };
    } else if (path.endsWith("/permissions")) {
      const id = path.split("/").at(-2);
      if (state.waitForPermissions) await state.waitForPermissions(id);
      status = state.permissionsStatus;
      data =
        status === 200
          ? { organization_id: id, actions: state.actions[id] || {}, revision: 1 }
          : {
              error: {
                code: "AUTHORIZATION_UNAVAILABLE",
                message: "Permission service unavailable",
              },
            };
    } else {
      data = { items: [], total: 0 };
    }
    await route.fulfill({ status, json: data });
  });
  return { state, requests };
}

test("new users see onboarding and organization creation without management menus", async ({
  page,
}) => {
  const { requests } = await fixture(page, { orgs: [] });
  await page.goto(origin + "/?page=users");
  await expect(page.getByRole("heading", { name: "你还没有加入任何组织" })).toBeVisible();
  await expect(page.getByRole("button", { name: "创建组织", exact: true })).toBeVisible();
  await expect(page.locator("aside nav button")).toHaveText(["概览"]);
  expect(requests).not.toContain("/api/v1/system/users");
  expect(requests.some((path) => path.endsWith("/permissions"))).toBe(false);
});

test("viewer deep links retain allowed pages and hide administration and Worker creation", async ({
  page,
}) => {
  await fixture(page);
  await page.goto(origin + "/?page=workers&workspace=org-a");
  await expect(page.getByRole("heading", { name: "Workers", exact: true })).toBeVisible();
  await expect(page.locator("aside nav button")).toHaveText([
    "概览",
    "Workers",
    "对象存储",
    "API 密钥",
    "资源目录",
    "权限检查",
  ]);
  await expect(page.getByRole("button", { name: "创建 Worker", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "平台管理", exact: true })).toHaveCount(0);
  await expect(page).toHaveURL(/page=workers/);
});

test("unauthorized direct URLs fall back without requesting protected page data", async ({
  page,
}) => {
  const { requests } = await fixture(page);
  for (const section of ["users", "orgs", "members", "roles", "audit"]) {
    await page.goto(origin + `/?page=${section}&workspace=org-a&view=system`);
    await expect(page.getByRole("heading", { name: "组织概览" })).toBeVisible();
    await expect(page).not.toHaveURL(new RegExp(`page=${section}`));
  }
  expect(requests).not.toContain("/api/v1/system/users");
  expect(requests).not.toContain("/api/v1/orgs/org-a/members");
  expect(requests).not.toContain("/api/v1/orgs/org-a/audit");
  expect(requests).not.toContain("/api/v1/orgs/org-a/workers/buckets");
});

test("organization admins see organization management without system administration", async ({
  page,
}) => {
  await fixture(page, { actions: { "org-a": admin } });
  await page.goto(origin + "/?page=members&workspace=org-a");
  await expect(page.getByRole("heading", { name: "组织成员", exact: true })).toBeVisible();
  for (const label of ["对象存储", "用户组", "角色授权", "审计记录"]) {
    await expect(
      page.locator("aside nav").getByRole("button", { name: label, exact: true }),
    ).toBeVisible();
  }
  await expect(
    page.locator("aside nav").getByRole("button", { name: "系统用户", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByRole("button", { name: "平台管理", exact: true })).toHaveCount(0);
});

test("system administration works before any organization exists", async ({ page }) => {
  await fixture(page, { orgs: [], systemAdmin: true });
  await page.goto(origin + "/");
  await page.getByRole("button", { name: "平台管理", exact: true }).click();
  await expect(page.getByRole("heading", { name: "组织目录" })).toBeVisible();
  await page.locator("aside nav").getByRole("button", { name: "系统用户", exact: true }).click();
  await expect(page.getByRole("heading", { name: "系统用户" })).toBeVisible();
  await page.locator("aside nav").getByRole("button", { name: "全平台审计", exact: true }).click();
  await expect(page.getByRole("heading", { name: "审计记录", exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "审计记录", exact: true })).toBeVisible();
});

test("switching organizations closes old management pages until new permissions arrive", async ({
  page,
}) => {
  let release;
  const waiting = new Promise((done) => {
    release = done;
  });
  await fixture(page, {
    orgs: [organization("org-a"), organization("org-b")],
    actions: { "org-a": admin, "org-b": viewer },
    waitForPermissions: (id) => (id === "org-b" ? waiting : Promise.resolve()),
  });
  await page.goto(origin + "/?page=members&workspace=org-a");
  await expect(page.getByRole("heading", { name: "组织成员", exact: true })).toBeVisible();
  await page.getByRole("combobox", { name: "切换组织" }).selectOption("org-b");
  await expect(page.getByRole("heading", { name: "组织成员", exact: true })).toHaveCount(0);
  await expect(
    page.locator("aside nav").getByRole("button", { name: "角色授权", exact: true }),
  ).toHaveCount(0);
  release();
  await expect(page.getByRole("heading", { name: "组织概览" })).toBeVisible();
  await expect(page).toHaveURL(/workspace=org-b/);
});

test("permission failures close organization navigation and allow retry", async ({ page }) => {
  const { state } = await fixture(page, { permissionsStatus: 503 });
  await page.goto(origin + "/?page=members&workspace=org-a");
  await expect(page.getByRole("alert")).toContainText("Permission service unavailable");
  await expect(page.locator("aside nav button")).toHaveText(["概览"]);
  state.permissionsStatus = 200;
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await expect(page.getByRole("heading", { name: "组织概览" })).toBeVisible();
});

test("returning to the window refreshes revoked organization privileges", async ({ page }) => {
  const { state } = await fixture(page, { actions: { "org-a": admin } });
  await page.goto(origin + "/?page=members&workspace=org-a");
  await expect(page.getByRole("heading", { name: "组织成员", exact: true })).toBeVisible();
  state.actions["org-a"] = viewer;
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(page.getByRole("heading", { name: "组织概览" })).toBeVisible();
  await expect(
    page.locator("aside nav").getByRole("button", { name: "组织成员", exact: true }),
  ).toHaveCount(0);
});

test("deployments can disable organization self-service", async ({ page }) => {
  await fixture(page, { orgs: [], selfService: false });
  await page.goto(origin + "/");
  await expect(page.getByRole("heading", { name: "你还没有加入任何组织" })).toBeVisible();
  await expect(page.getByRole("button", { name: "创建组织", exact: true })).toHaveCount(0);
  await expect(page.getByText("把你的用户 ID 提供给管理员")).toBeVisible();
});

test("focus revalidation preserves unsaved forms when permissions are unchanged", async ({ page }) => {
  await fixture(page, { actions: { "org-a": admin } });
  await page.goto(origin + "/?page=workers&workspace=org-a");
  await page.getByRole("button", { name: "创建 Worker", exact: true }).click();
  const input = page.getByRole("dialog").getByRole("textbox", { name: "Worker 名称", exact: true });
  await input.fill("unsaved-worker");
  const refreshed = page.waitForResponse((response) => response.url().endsWith("/permissions"));
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await refreshed;
  await expect(input).toHaveValue("unsaved-worker");
});
