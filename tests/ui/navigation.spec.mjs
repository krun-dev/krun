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

test("organization short names follow the name until manually edited", async ({ page }) => {
  const { state } = await fixture(page, { orgs: [] });
  await page.route("**/api/v1/orgs", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();

    const created = { ...organization("org-new"), ...route.request().postDataJSON() };
    state.orgs.push(created);
    await route.fulfill({ status: 201, json: created });
  });
  await page.goto(origin + "/?page=overview");
  await page.getByRole("button", { name: "创建组织", exact: true }).click();
  const dialog = page.getByRole("dialog");
  const name = dialog.getByRole("textbox", { name: "组织名称", exact: true });
  const shortName = dialog.getByRole("textbox", { name: /组织短名/ });

  await name.fill("Platform Engineering");
  await expect(shortName).toHaveValue("platform-engineering");
  await name.fill("平台工程");
  await expect(shortName).toHaveValue("ping-tai-gong-cheng");
  await name.fill(`${"a".repeat(31)} Next`);
  await expect(shortName).toHaveValue("a".repeat(31));

  await shortName.fill("platform-team");
  await name.fill("Infrastructure Team");
  await expect(shortName).toHaveValue("platform-team");
  const request = page.waitForRequest(
    (req) => req.method() === "POST" && req.url().endsWith("/api/v1/orgs"),
  );
  await dialog.getByRole("button", { name: "创建组织", exact: true }).click();
  expect((await request).postDataJSON()).toEqual({
    name: "Infrastructure Team",
    short_name: "platform-team",
  });
  await expect(dialog).toBeHidden();
});

test("Worker creation sends the suggested locator shown in the form", async ({ page }) => {
  await fixture(page, { actions: { "org-a": admin } });
  await page.addInitScript(() => localStorage.setItem("krun.theme", "dark"));
  await page.route("**/api/v1/orgs/org-a/workers/network-profiles", (route) =>
    route.fulfill({ json: { items: [{ id: "test", name: "Test", default: true }] } }),
  );
  await page.route("**/api/v1/orgs/org-a/workers", (route) => {
    if (route.request().method() !== "POST") return route.fallback();

    return route.fulfill({ status: 201, json: { id: "worker-new", locator: "image-resize-2" } });
  });
  await page.goto(origin + "/?page=workers&workspace=org-a");
  await page.getByRole("button", { name: "创建 Worker", exact: true }).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "Worker 名称", exact: true }).fill("Image_Resize 2");
  await expect(dialog.getByRole("textbox", { name: /Worker 定位符/ })).toHaveValue(
    "image-resize-2",
  );
  const request = page.waitForRequest(
    (req) => req.method() === "POST" && req.url().endsWith("/api/v1/orgs/org-a/workers"),
  );
  await dialog.getByRole("button", { name: "创建 Worker", exact: true }).click();
  expect((await request).postDataJSON()).toEqual({
    name: "Image_Resize 2",
    locator: "image-resize-2",
  });
  await expect(dialog).toBeHidden();
});

for (const lostResponse of [false, true]) {
  test(`Worker creation retries network configuration after ${lostResponse ? "a lost response" : "a failed update"}`, async ({
    page,
  }) => {
    await fixture(page, { actions: { "org-a": admin } });
    if (lostResponse) {
      await page.addInitScript(() => localStorage.setItem("krun.theme", "dark"));
    }
    const worker = { id: "worker-new", locator: "image-resize", name: "Image Resize" };
    const profile = { id: "office", name: "Office" };
    let creates = 0;
    const updates = [];
    await page.route("**/api/v1/orgs/org-a/workers/network-profiles", (route) =>
      route.fulfill({ json: { items: [{ id: "test", name: "Test", default: true }, profile] } }),
    );
    await page.route("**/api/v1/orgs/org-a/workers", (route) => {
      if (route.request().method() !== "POST") {
        return route.fallback();
      }
      creates++;
      return route.fulfill({ status: 201, json: worker });
    });
    await page.route("**/workers/" + worker.id + "/network-profile", (route) => {
      updates.push(route.request().postDataJSON());
      if (updates.length === 1) {
        if (lostResponse) {
          return route.abort("failed");
        }
        return route.fulfill({
          status: 503,
          json: { error: { code: "NETWORK_PROFILE_UNAVAILABLE", message: "Profile unavailable" } },
        });
      }
      if (lostResponse) {
        return route.fulfill({
          status: 409,
          json: { error: { code: "REVISION_CONFLICT", message: "Network configuration changed" } },
        });
      }
      return route.fulfill({ json: { revision: 1, network_profile: profile } });
    });
    await page.route("**/workers/" + worker.id + "/domains", (route) => {
      return route.fulfill({ json: { revision: 1, network_profile: profile } });
    });

    await page.goto(origin + "/?page=workers&workspace=org-a");
    await page.getByRole("button", { name: "创建 Worker", exact: true }).click();
    const dialog = page.getByRole("dialog");
    const name = dialog.getByRole("textbox", { name: "Worker 名称", exact: true });
    await name.fill(worker.name);
    await dialog.getByRole("button", { name: "Office", exact: true }).click();
    await dialog.getByRole("button", { name: "创建 Worker", exact: true }).click();
    await expect(dialog.getByRole("alert")).toContainText(
      lostResponse ? "网络连接失败" : "Profile unavailable",
      { timeout: 3000 },
    );
    await expect(name).toBeDisabled();
    await expect(page).not.toHaveURL(/worker=image-resize/);

    await dialog.getByRole("button", { name: "重试网络配置", exact: true }).click();
    await expect(dialog).toBeHidden();
    await expect(page).toHaveURL(/worker=image-resize/);
    expect(creates).toBe(1);
    expect(updates).toEqual([
      { network_profile: profile.id, expected_revision: 0 },
      { network_profile: profile.id, expected_revision: 0 },
    ]);
  });
}

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
    "Hyperdrive",
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

test("management pages show names and keep IDs available for CLI and forms", async ({
  page,
  context,
}) => {
  await fixture(page, {
    orgs: [{ ...organization("org-a"), name: "平台工程", short_name: "platform" }],
    actions: { "org-a": admin },
  });
  await page.route("**/api/v1/orgs/platform/permissions", (route) =>
    route.fulfill({ json: { organization_id: "org-a", actions: admin, revision: 1 } }),
  );
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.route("**/api/v1/orgs/platform/groups", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: "grp-on-call",
            name: "值班团队",
            members: ["user-alice", "user-legacy"],
            member_names: { "user-alice": "Alice" },
            revision: 2,
          },
        ],
      },
    }),
  );
  await page.route("**/api/v1/orgs/platform/role-assignments", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            assignment_id: "assignment-on-call",
            subject_kind: "group",
            subject_id: "grp-on-call",
            subject_name: "值班团队",
            role_id: "viewer",
            scope_kind: "organization",
            scope_id: "org-a",
            scope_name: "平台工程",
            conditions: {},
            revision: 3,
          },
        ],
      },
    }),
  );

  await page.goto(origin + "/?workspace=platform");
  await expect(page.getByText("当前身份", { exact: true })).toBeVisible();
  await expect(page.getByText("platform", { exact: true })).toBeVisible();
  await expect(page.getByText(/权限修订|每次请求重新检查|v0\.1/)).toHaveCount(0);
  await page.getByRole("button", { name: "复制用户 ID", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("user-test");

  await page.getByRole("button", { name: "用户组", exact: true }).click();
  await expect(page.getByText("2 位成员", { exact: true })).toBeVisible();
  await expect(page.getByText("Alice", { exact: true })).toBeVisible();
  await expect(page.getByText("user-legacy", { exact: true })).toBeVisible();
  await expect(page.getByText(/修订|user-alice|grp-on-call/)).toHaveCount(0);
  await page.getByRole("button", { name: "复制用户组 ID", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("grp-on-call");

  await page.getByRole("button", { name: "角色授权", exact: true }).click();
  const row = page.getByRole("row").filter({ hasText: "值班团队" });
  await expect(row).toContainText("平台工程");
  await expect(row).not.toContainText("org-a");
  await row.getByRole("button", { name: "复制授权对象 ID", exact: true }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("grp-on-call");
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
  await expect(page.getByText(/权限修订|默认拒绝|由服务端解析|ALLOWED/)).toHaveCount(0);
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
  for (const section of ["users", "orgs", "members", "roles", "audit", "system-config"]) {
    await page.goto(origin + `/?page=${section}&workspace=org-a&view=system`);
    await expect(page.getByRole("heading", { name: "组织概览" })).toBeVisible();
    await expect(page).not.toHaveURL(new RegExp(`page=${section}`));
  }
  expect(requests).not.toContain("/api/v1/system/users");
  expect(requests).not.toContain("/api/v1/system/config");
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
  await page.getByRole("button", { name: "切换组织: org-a", exact: true }).click();
  await expect(page.getByRole("menuitemradio", { name: "org-a" })).toBeChecked();
  await page.getByRole("menuitemradio", { name: "org-b" }).click();
  await expect(page.getByRole("heading", { name: "组织成员", exact: true })).toHaveCount(0);
  await expect(
    page.locator("aside nav").getByRole("button", { name: "角色授权", exact: true }),
  ).toHaveCount(0);
  release();
  await expect(page.getByRole("heading", { name: "组织概览" })).toBeVisible();
  await expect(page).toHaveURL(/workspace=org-b/);
  await expect(page.getByRole("button", { name: "切换组织: org-b", exact: true })).toBeVisible();
  await expect(page.getByRole("menu")).toHaveCount(0);
});

test("organization switcher supports keyboard selection and escape", async ({ page }) => {
  await fixture(page, {
    orgs: [organization("org-a"), organization("org-b")],
    actions: { "org-a": viewer, "org-b": viewer },
  });
  await page.goto(origin + "/?workspace=org-a");
  const trigger = page.getByRole("button", { name: "切换组织: org-a", exact: true });
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
  await expect(page.getByRole("button", { name: "切换组织: org-b", exact: true })).toBeFocused();
});

test("selecting the current organization exits platform management on mobile", async ({ page }) => {
  await fixture(page, { systemAdmin: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(origin + "/?workspace=org-a&view=system");
  await expect(page.getByRole("heading", { name: "组织目录" })).toBeVisible();
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByRole("button", { name: "切换组织: org-a", exact: true }).click();
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

test("system administration lists Workers, buckets, and keys across organizations", async ({
  page,
}) => {
  const { requests } = await fixture(page, {
    systemAdmin: true,
    orgs: [organization("org-a")],
  });
  const workerA = {
    id: "worker-a",
    name: "Worker A",
    organization_id: "org-a",
    organization_name: "Organization A",
  };
  const workerB = {
    id: "worker-b",
    name: "Worker B",
    organization_id: "org-b",
    organization_name: "Organization B",
  };
  await page.route("**/api/v1/system/inventory/workers?*", (route) => {
    const query = new URL(route.request().url()).searchParams;
    return route.fulfill({
      json:
        query.has("after") && query.get("after") !== ""
          ? { items: [workerB] }
          : { items: [workerA], next_after: workerA.id },
    });
  });
  await page.route("**/api/v1/system/inventory/buckets?*", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: "bucket-b",
            name: "backup",
            organization_id: "org-b",
            organization_name: "Organization B",
            enabled: false,
          },
        ],
      },
    }),
  );
  await page.route("**/api/v1/system/inventory/api-keys?*", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: "key-b",
            name: "Backup key",
            owner_user_id: "owner-b",
            organization_id: "org-b",
            organization_name: "Organization B",
            prefix: "krun_key_example",
            revoked: false,
            expires_at: null,
            policy_revision: 1,
            policy: { schema_version: 1, statements: [] },
          },
        ],
      },
    }),
  );

  await page.goto(origin + "/?view=system&page=workers");
  await expect(page.getByRole("heading", { name: "全平台 Workers" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Worker A" })).toHaveAttribute(
    "href",
    "/?view=system&page=workers&inventory_org=org-a&inventory_resource=worker-a&workspace=org-a",
  );
  await page.getByRole("button", { name: "下一页" }).click();
  await expect(page.getByRole("row", { name: /Worker B.*Organization B/ })).toBeVisible();
  await expect(page.getByRole("link", { name: "Worker B" })).toHaveCount(0);

  const nav = page.locator("aside nav");
  await nav.getByRole("button", { name: "全平台 Bucket" }).click();
  await expect(page.getByRole("row", { name: /backup.*Organization B.*已停用/ })).toBeVisible();
  await nav.getByRole("button", { name: "全平台 API 密钥" }).click();
  await expect(page.getByRole("row", { name: /Backup key.*Organization B/ })).toBeVisible();
  await expect(page.getByRole("button", { name: "撤销", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "查看权限" }).click();
  await expect(page.getByRole("dialog").getByText("owner-b")).toBeVisible();
  expect(requests).not.toContain("/api/v1/system/api-keys");
  await page.goto(
    origin + "/?view=system&page=workers&inventory_org=org-b&inventory_resource=worker-b",
  );
  await expect(page.getByText("资源不存在或无权访问。")).toBeVisible();
  await page.getByRole("button", { name: "返回全平台列表" }).click();
  await expect(page.getByRole("heading", { name: "全平台 Workers" })).toBeVisible();
  expect(requests.some((path) => path.startsWith("/api/v1/orgs/org-b/"))).toBe(false);
});

test("platform detail reuses Worker invocation and Bucket controls for the owning organization", async ({
  page,
}) => {
  await fixture(page, {
    systemAdmin: true,
    orgs: [organization("org-current"), organization("org-owner")],
    actions: {
      "org-owner": {
        ...admin,
        "storage.bucket.update": true,
        "storage.object.write": true,
      },
    },
  });
  const resource = { organization_id: "org-owner", organization_name: "Owning organization" };
  const worker = { ...resource, id: "worker-owned", name: "Owned Worker" };
  const bucket = {
    ...resource,
    id: "bucket-owned",
    name: "owned-bucket",
    enabled: false,
    revision: 1,
    created_at: "2026-01-01T00:00:00Z",
  };
  const base = "/api/v1/orgs/org-owner";
  const workerBase = base + "/workers/" + worker.id;
  const bucketBase = base + "/buckets/";
  const calls = [];
  await page.route(
    (url) => url.pathname.startsWith("/api/v1/system/inventory/"),
    (route) => {
      const path = new URL(route.request().url()).pathname;
      return route.fulfill({ json: { items: path.endsWith("/workers") ? [worker] : [bucket] } });
    },
  );
  await page.route(
    (url) => url.pathname.startsWith(base + "/"),
    async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      let json;
      switch (url.pathname) {
        case base + "/workers":
          expect(url.searchParams.get("worker")).toBe(worker.id);
          json = { items: [worker] };
          break;
        case workerBase + "/domains":
          json = { catalog: [], network_profile: {} };
          break;
        case workerBase + "/repository":
          json = { source: null, revision: 1 };
          break;
        case workerBase + "/overview":
          json = {
            builds: [],
            operations: [],
            versions: {},
            environments: [
              {
                environment: {
                  name: "production",
                  enabled: true,
                  version_id: "version-owned",
                  confirmed_version_id: "version-owned",
                  deployment_revision: 1,
                },
                history: [],
              },
            ],
          };
          break;
        case workerBase + "/environments/production/invoke":
          expect(request.method()).toBe("POST");
          calls.push(url.pathname);
          json = { status: 200, body: "worker-response", headers: {}, truncated: false };
          break;
        case bucketBase + bucket.id:
          json = bucket;
          break;
        case bucketBase + bucket.name:
          expect(request.method()).toBe("PATCH");
          expect(request.postDataJSON()).toEqual({ enabled: true, revision: 1 });
          calls.push(url.pathname);
          bucket.enabled = true;
          bucket.revision = 2;
          json = bucket;
          break;
        case bucketBase + bucket.name + "/objects":
          json = {
            objects: [],
            delimited_prefixes: [],
            truncated: false,
            delivery_available: false,
          };
          break;
        default:
          return route.fallback();
      }
      await route.fulfill({ json });
    },
  );

  await page.goto(origin + "/?view=system&page=workers&workspace=org-current");
  await page.getByRole("link", { name: worker.name }).click();
  await expect(page).toHaveURL(/view=system.*inventory_org=org-owner.*workspace=org-current/);
  await expect(page.getByRole("heading", { name: worker.name })).toBeVisible();
  await page.getByRole("tab", { name: "调用测试" }).click();
  await page.getByRole("button", { name: "发送请求" }).click();
  await expect(page.getByText("worker-response")).toBeVisible();
  await page.reload();
  await expect(page.getByRole("tab", { name: "调用测试" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await page.getByRole("button", { name: "返回Worker列表" }).click();
  await expect(page.getByRole("heading", { name: "全平台 Workers" })).toBeVisible();
  await expect(page).not.toHaveURL(/inventory_resource|worker_tab|environment/);

  await page.locator("aside nav").getByRole("button", { name: "全平台 Bucket" }).click();
  await page.getByRole("link", { name: bucket.name }).click();
  await expect(page).toHaveURL(/view=system.*inventory_org=org-owner/);
  await page.getByRole("button", { name: "启用", exact: true }).click();
  await page.getByRole("dialog", { name: "启用桶" }).getByRole("button", { name: "确认" }).click();
  await expect(page.locator("button").filter({ hasText: "上传文件" })).toBeVisible();
  await expect(page.getByText("目录为空")).toBeVisible();
  await page.getByRole("button", { name: "返回桶列表" }).click();
  await expect(page.getByRole("heading", { name: "全平台 Bucket" })).toBeVisible();
  expect(calls).toEqual([workerBase + "/environments/production/invoke", bucketBase + bucket.name]);
});

test("system administrators can read runtime pool configuration without an organization", async ({
  page,
}) => {
  await fixture(page, { systemAdmin: true, orgs: [] });
  await page.route("**/api/v1/system/config", (route) =>
    route.fulfill({
      json: {
        workers: {
          builder_mode: "kubernetes",
          runtime_api_override: true,
          preview_limit: 32,
          validator_pool: "private-b",
          runtime_pools: [
            {
              id: "public-a",
              default: true,
              access: ["public"],
              private_egress: "deny",
              public_egress: "direct",
              validator_configured: false,
              network_profiles: ["standard"],
            },
            {
              id: "private-b",
              default: false,
              access: ["private"],
              private_egress: "direct",
              public_egress: "deny",
              validator_configured: true,
              network_profiles: ["private-only"],
            },
          ],
        },
        deployment: { runtime_replicas: 3, validator_replicas: 2 },
      },
    }),
  );
  await page.goto(origin + "/?view=system&page=orgs");
  await page.locator("aside nav").getByRole("button", { name: "系统配置" }).click();
  await expect(page.getByRole("heading", { name: "系统配置" })).toBeVisible();
  await expect(page.getByText("独立校验池 private-b", { exact: true })).toBeVisible();
  await expect(page.getByText("自定义地址（已隐藏）", { exact: true })).toBeVisible();
  await expect(page.getByRole("row", { name: /public-a.*默认池.*standard/ })).toBeVisible();
  await expect(page.getByRole("row", { name: /private-b.*正在使用.*private-only/ })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "运行资源池" })).toBeVisible();
});

for (const theme of ["light", "dark"]) {
  test(`Hyperdrive can create a configuration in ${theme} mode`, async ({ page }) => {
    await page.addInitScript((value) => localStorage.setItem("krun.theme", value), theme);
    await fixture(page, { actions: { "org-a": admin } });
    const items = [];
    await page.route(new RegExp("/api/v1/orgs/org-a/hyperdrive$"), async (route) => {
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON();
        expect(body.origin.password).toBe("fixture-password");
        const { password, ...origin } = body.origin;
        items.push({ id: "a".repeat(32), name: body.name, ...origin });
        await route.fulfill({ json: items.at(-1) });
      } else {
        await route.fulfill({ json: { items } });
      }
    });
    await page.goto(origin + "/?page=hyperdrive&workspace=org-a");
    await expect(page.getByRole("heading", { name: "Hyperdrive", exact: true })).toBeVisible();
    await expect
      .poll(() => page.locator("html").evaluate((element) => element.classList.contains("dark")))
      .toBe(theme === "dark");
    await page.getByRole("button", { name: "创建 Hyperdrive 配置" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("名称").fill("orders");
    await dialog.getByLabel("源库域名").fill("orders.example.test");
    await dialog.getByLabel("库名").fill("orders");
    await dialog.getByLabel("源库用户名").fill("app");
    await dialog.getByLabel("源库密码").fill("fixture-password");
    await dialog.getByRole("button", { name: "保存" }).click();
    await expect(page.getByRole("cell", { name: "orders", exact: true })).toBeVisible();
    await expect(page.getByText("fixture-password")).toHaveCount(0);
  });
}
