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
  "worker.list": true,
  "model.list": true,
  "apiKey.create": true,
  "storage.object.read": true,
};
const admin = {
  ...viewer,
  "worker.create": true,
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
    } else if (path === "/api/v1/roles" && state.permissionCatalog) {
      data = state.permissionCatalog;
    } else if (path === "/api/auth/csrf") {
      data = { csrf_token: "ui-fixture" };
    } else if (path === "/api/v1/authz/check") {
      data = {
        allowed: true,
        user_allowed: true,
        key_allowed: true,
        reason: "ALLOWED",
        revision: 1,
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
    "权限检查",
    "API 密钥",
    "Git 仓库授权",
  ]);
  await expect(page.getByRole("button", { name: "创建 Worker", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "平台管理", exact: true })).toHaveCount(0);
  await expect(page).toHaveURL(/page=workers/);
});

test("removed resource catalog links open the current organization overview", async ({ page }) => {
  const { requests } = await fixture(page);
  await page.goto(origin + "/?page=resources&workspace=org-a");
  await expect(page.getByRole("heading", { name: "组织概览" })).toBeVisible();
  await expect(page).toHaveURL(origin + "/?workspace=org-a");
  expect(requests).not.toContain("/api/v1/orgs/org-a/workers");
  expect(requests).not.toContain("/api/v1/orgs/org-a/models");
});

test("permission forms use the environments advertised by the API", async ({ page }) => {
  await fixture(page, {
    actions: { "org-a": admin },
    permissionCatalog: {
      actions: { "worker.read": ["worker"] },
      environments: ["global", "production"],
    },
  });

  await page.goto(origin + "/?page=check&workspace=org-a");
  const target = page.getByRole("combobox", { name: "目标环境", exact: true });
  await expect(target.locator("option")).toHaveText(["全局", "生产"]);
  await expect(target).toHaveValue("global");
  await target.selectOption("production");
  const checkRequest = page.waitForRequest((request) =>
    request.url().endsWith("/api/v1/authz/check"),
  );
  await page.getByRole("button", { name: "检查权限", exact: true }).click();
  expect((await checkRequest).postDataJSON().environment).toBe("production");
  const result = page.getByRole("heading", { name: "允许访问", exact: true });
  await expect(result).toBeVisible();
  await page.getByRole("button", { name: "切换深色主题", exact: true }).click();
  await expect(result).toBeVisible();
  await expect(page.locator("html")).toHaveClass(/dark/);

  await page.goto(origin + "/?page=roles&workspace=org-a");
  await page.getByRole("button", { name: "授予角色", exact: true }).first().click();
  const grant = page.getByRole("dialog");
  await expect(grant.getByRole("checkbox")).toHaveCount(2);
  await expect(grant.getByRole("checkbox", { name: "全局", exact: true })).toBeVisible();
  await expect(grant.getByRole("checkbox", { name: "生产", exact: true })).toBeVisible();

  await page.goto(origin + "/?page=keys&workspace=org-a");
  await page.getByRole("button", { name: "创建 API Key", exact: true }).first().click();
  const conditions = page.getByRole("group", { name: /环境条件/ });
  await expect(conditions.getByRole("checkbox")).toHaveCount(2);
  await expect(conditions.getByRole("checkbox", { name: "全局", exact: true })).toBeVisible();
  await expect(conditions.getByRole("checkbox", { name: "生产", exact: true })).toBeVisible();
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
  expect(requests).not.toContain("/api/v1/orgs/org-a/buckets");
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

test("audit downloads the same snapshot with the selected redaction mode", async ({ page }) => {
  await fixture(page, { actions: { "org-a": admin } });
  const path = "/api/v1/orgs/org-a/audit";
  const asOf = "2026-01-01T00:00:00Z";
  const exported = [];
  await page.route(
    (url) => url.pathname === path,
    async (route) => {
      const cursor = new URL(route.request().url()).searchParams.get("cursor");
      await route.fulfill({
        json: {
          items: [
            {
              id: cursor ? "audit-two" : "audit-one",
              time: asOf,
              actor: "user-test",
              credential: "session",
              action: "worker.update",
              resource: "worker-test",
              allowed: true,
              request_id: "request-test",
            },
          ],
          total: 51,
          cursor: cursor || "first-page",
          has_more: !cursor,
          next_cursor: cursor ? "" : "second-page",
          as_of: asOf,
        },
      });
    },
  );
  await page.route(
    (url) => url.pathname === path + "/export",
    async (route) => {
      const url = new URL(route.request().url());
      const redact = url.searchParams.get("redact");
      exported.push({ cursor: url.searchParams.get("cursor"), redact });
      await route.fulfill({
        contentType: "text/csv; charset=utf-8",
        body: redact === "true" ? "actor\n[REDACTED]\n" : "actor\nuser-test\n",
      });
    },
  );

  await page.goto(origin + "/?page=audit&workspace=org-a");
  await page.getByRole("button", { name: "下一页" }).click();
  await expect(page.getByText("第 2 页", { exact: false })).toBeVisible();
  const redact = page.getByRole("checkbox", { name: "脱敏导出" });
  const download = page.getByRole("button", { name: "导出 CSV" });
  await expect(redact).toBeChecked();

  async function downloadContents() {
    const event = page.waitForEvent("download");
    await download.click();
    const file = await event;
    expect(file.suggestedFilename()).toBe("krun-audit.csv");
    return readFile(await file.path(), "utf8");
  }

  expect(await downloadContents()).toBe("actor\n[REDACTED]\n");
  await page.getByRole("button", { name: "切换深色主题" }).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await redact.click();
  await expect(redact).not.toBeChecked();
  expect(await downloadContents()).toBe("actor\nuser-test\n");
  expect(exported).toEqual([
    { cursor: "first-page", redact: "true" },
    { cursor: "first-page", redact: "false" },
  ]);
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
  await page.getByRole("button", { name: "切换组织：org-a", exact: true }).click();
  await expect(page.getByRole("menuitemradio", { name: "org-a" })).toBeChecked();
  await page.getByRole("menuitemradio", { name: "org-b" }).click();
  await expect(page.getByRole("heading", { name: "组织成员", exact: true })).toHaveCount(0);
  await expect(
    page.locator("aside nav").getByRole("button", { name: "角色授权", exact: true }),
  ).toHaveCount(0);
  release();
  await expect(page.getByRole("heading", { name: "组织概览" })).toBeVisible();
  await expect(page).toHaveURL(/workspace=org-b/);
  await expect(page.getByRole("button", { name: "切换组织：org-b", exact: true })).toBeVisible();
  await expect(page.getByRole("menu")).toHaveCount(0);
});

test("organization switcher supports keyboard selection and escape", async ({ page }) => {
  await fixture(page, {
    orgs: [organization("org-a"), organization("org-b")],
    actions: { "org-a": viewer, "org-b": viewer },
  });
  await page.goto(origin + "/?workspace=org-a");
  const trigger = page.getByRole("button", { name: "切换组织：org-a", exact: true });
  await trigger.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("menuitemradio", { name: "org-a" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(trigger).toBeFocused();
  await expect(page.getByRole("menu")).toHaveCount(0);
  await page.keyboard.press("Enter");
  await expect(page.getByRole("menuitemradio", { name: "org-a" })).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(page.getByRole("menuitemradio", { name: "org-b" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/workspace=org-b/);
  await expect(page.getByRole("button", { name: "切换组织：org-b", exact: true })).toBeFocused();
});

test("selecting the current organization exits platform management on mobile", async ({ page }) => {
  await fixture(page, { systemAdmin: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(origin + "/?workspace=org-a&view=system");
  await expect(page.getByRole("heading", { name: "组织目录" })).toBeVisible();
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByRole("button", { name: "切换组织：org-a", exact: true }).click();
  await page.getByRole("menuitemradio", { name: "org-a" }).click();
  await expect(page.getByRole("heading", { name: "组织概览" })).toBeVisible();
  await expect(page).not.toHaveURL(/view=system/);
  await expect(page.locator("aside")).toBeHidden();
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

test("focus revalidation preserves unsaved forms when permissions are unchanged", async ({
  page,
}) => {
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
