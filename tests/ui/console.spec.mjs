import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

// These tests use real authentication, PostgreSQL, builders, S3 and workerd.
// No network requests are mocked. Run after deploying the dedicated kind E2E
// stack, with its same-origin static console forwarded to KRUN_UI_ORIGIN.
test.describe.configure({ mode: "serial" });
const pageErrors = new WeakMap();
let cookies, org, workflow, resources;
const run = randomUUID().slice(0, 8);
const base = () => `/api/v1/orgs/${org.id}`;
const workerBase = () => `${base()}/workers/${workflow.id}`;
const locationFor = (id) =>
  `/?page=workers&workspace=${org.id}${id ? "&worker=" + id : ""}`;
async function api(context, path, method = "GET", input) {
  const headers = {
    origin: new URL(process.env.KRUN_UI_ORIGIN || "http://127.0.0.1:4380")
      .origin,
  };
  if (method !== "GET") {
    const csrf = await context.request.post("/api/auth/csrf", { headers });
    expect(csrf.ok()).toBeTruthy();
    headers["X-CSRF-Token"] = (await csrf.json()).csrf_token;
    headers.origin = new URL(
      process.env.KRUN_UI_ORIGIN || "http://127.0.0.1:4380",
    ).origin;
  }
  const response = await context.request.fetch(path, {
    method,
    headers,
    data: input,
  });
  expect(
    response.ok(),
    `${method} ${path}: ${await response.text()}`,
  ).toBeTruthy();
  return response.status() === 204 ? undefined : response.json();
}
async function fillVariables(page, variables) {
  const section = page.getByRole("region", { name: "环境变量", exact: true });
  while (await section.getByRole("button", { name: /移除变量/ }).count()) {
    await section
      .getByRole("button", { name: /移除变量/ })
      .first()
      .click();
  }
  let index = 0;
  for (const [name, value] of Object.entries(variables)) {
    index += 1;
    await section.getByRole("button", { name: "添加变量", exact: true }).click();
    await section.getByRole("textbox", { name: "变量名 " + index, exact: true }).fill(name);
    await section
      .getByRole("combobox", { name: "值类型 " + index, exact: true })
      .selectOption(typeof value === "string" ? "text" : "json");
    await section
      .getByRole("textbox", { name: "变量值 " + index, exact: true })
      .fill(typeof value === "string" ? value : JSON.stringify(value));
  }
}
async function login(page) {
  await page.goto("/?page=login");
  await page
    .getByRole("button", { name: /本地开发登录|kind 开发登录/ })
    .click();
  await expect(page.getByRole("button", { name: "退出登录" })).toBeVisible();
}
async function currentEnvironment(context) {
  return (await api(context, workerBase() + "/environments")).items.find(
    (e) => e.name === "production",
  );
}
async function waitRevision(context, revision) {
  await expect
    .poll(
      async () => {
        const e = await currentEnvironment(context);
        return e.version_id && e.version_id === e.confirmed_version_id
          ? e.deployment_revision
          : 0;
      },
      { timeout: 150_000, intervals: [1000, 2000] },
    )
    .toBe(revision);
}
async function publishUI(page, context, revision) {
  await page.getByRole("tab", { name: "构建", exact: true }).click();
  await page.getByRole("button", { name: "发布", exact: true }).first().click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "确认发布", exact: true })
    .click();
  await waitRevision(context, revision);
}
test.beforeAll(async ({ browser, baseURL }) => {
  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();
  await login(page);
  cookies = await context.cookies();
  org = await api(context, "/api/v1/orgs", "POST", {
    name: "UI scaling " + run,
  });
  resources = [];
  for (let offset = 0; offset < 105; offset += 7) {
    const batch = await Promise.all(
      Array.from({ length: Math.min(7, 105 - offset) }, (_, i) =>
        api(context, base() + "/workers", "POST", {
          name: `UI site ${run} ${offset + i}`,
        }),
      ),
    );
    resources.push(...batch);
  }
  workflow = await api(context, base() + "/workers", "POST", {
    name: "UI workflow " + run,
  });
  // Materialize its placement so the UI can configure a repository and run a
  // real build; the UI below performs binding, building and publishing itself.
  const profiles = await api(context, base() + "/workers/network-profiles");
  await api(context, workerBase() + "/network-profile", "PUT", {
    network_profile:
      profiles.items.find((p) => p.default)?.id || profiles.items[0].id,
    expected_revision: 0,
  });
  await context.close();
});
test.beforeEach(async ({ context, page }) => {
  const errors = [];
  pageErrors.set(page, errors);
  page.on("pageerror", (error) => errors.push(error.message));
  await context.addCookies(cookies);
  await page.addInitScript(() =>
    localStorage.setItem("krun.language", "zh-CN"),
  );
});
test.afterEach(async ({ page }) => {
  expect(pageErrors.get(page), "browser JavaScript exceptions").toEqual([]);
});
test("static export, login, language persistence and logout", async ({
  browser,
  baseURL,
}) => {
  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();
  const response = await page.goto("/");
  expect(response.status()).toBe(200);
  await expect(page.getByRole("heading", { name: "Run anything." })).toBeVisible();
  await page.getByRole("link", { name: "进入控制台" }).click();
  await expect(page.getByRole("heading", { name: "登录组织" })).toBeVisible();
  await page.getByRole("combobox", { name: "语言" }).selectOption("en");
  await page.reload();
  await expect(page.getByRole("combobox", { name: "Language" })).toHaveValue(
    "en",
  );
  await page.getByRole("combobox", { name: "Language" }).selectOption("zh-CN");
  await login(page);
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect(page.getByRole("heading", { name: "Run anything." })).toBeVisible();
  await page.getByRole("link", { name: "进入控制台" }).click();
  await expect(page.getByRole("heading", { name: "登录组织" })).toBeVisible();
  await context.close();
});
test("Workers paginate through all 106 sites", async ({
  page,
}) => {
  await page.goto(locationFor());
  await expect(page.locator("tbody tr")).toHaveCount(100);
  await page.getByRole("button", { name: "下一页", exact: true }).click();
  await expect(page.locator("tbody tr")).toHaveCount(6);
  await page.getByRole("button", { name: "上一页", exact: true }).click();
  await expect(page.locator("tbody tr")).toHaveCount(100);
});
test("deep link opens a service outside the first page", async ({ page }) => {
  const last = [...resources, workflow]
    .sort((a, b) => a.id.localeCompare(b.id))
    .at(-1);
  await page.goto(locationFor(last.id));
  await expect(
    page.getByRole("tab", { name: "配置版本", exact: true }),
  ).toBeVisible();
  await expect(page.getByText(last.name, { exact: true })).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("tab", { name: "配置版本", exact: true }),
  ).toBeVisible();
});
test("UI binds repository, builds, publishes and invokes a real Worker", async ({
  page,
  context,
}) => {
  await page.goto(locationFor(workflow.id));
  await page.getByRole("tab", { name: "代码源", exact: true }).click();
  await page
    .getByRole("combobox", { name: "仓库平台" })
    .selectOption("kind-git");
  await page
    .getByRole("textbox", { name: "仓库地址", exact: true })
    .fill("fixture/worker");
  await page.getByRole("button", { name: "保存仓库配置", exact: true }).click();
  await expect
    .poll(
      async () => (await api(context, workerBase() + "/repository")).revision,
    )
    .toBe(1);
  await page.getByRole("tab", { name: "构建", exact: true }).click();
  await page
    .getByRole("button", { name: /构建新版本|从仓库构建|新建构建/ })
    .first()
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "开始构建", exact: true })
    .click();
  await expect
    .poll(
      async () =>
        (await api(context, workerBase() + "/builds")).items[0]?.state,
      { timeout: 150_000, intervals: [1000, 2000] },
    )
    .toBe("succeeded");
  await publishUI(page, context, 1);
  await page.getByRole("tab", { name: "调用测试", exact: true }).click();
  await page
    .getByRole("textbox", { name: "请求路径", exact: true })
    .fill("/ui-flow?run=" + run);
  await page.getByRole("button", { name: "发送请求", exact: true }).click();
  await expect(page.getByText(/repository-wrangler/)).toBeVisible();
  await page.screenshot({
    path: ".data/performance-ui-workflow.png",
    fullPage: true,
  });
});
test("UI saves variables, publishes new configuration, and rolls back", async ({
  page,
  context,
}) => {
  await page.goto(locationFor(workflow.id));
  await page.getByRole("tab", { name: "配置版本", exact: true }).click();
  await page.getByText("管理环境草稿", { exact: true }).click();
  await fillVariables(page, { UI_MARKER: "revision-two" });
  await page.getByRole("button", { name: "保存环境变量", exact: true }).click();
  await expect
    .poll(async () => (await currentEnvironment(context)).variables.UI_MARKER)
    .toBe("revision-two");
  await publishUI(page, context, 2);
  await expect(
    page.getByRole("button", { name: "回滚到此版本", exact: true }).last(),
  ).toBeEnabled();
  await page
    .getByRole("button", { name: "回滚到此版本", exact: true })
    .last()
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "确认回滚", exact: true })
    .click();
  await waitRevision(context, 3);
  const version = await api(
    context,
    workerBase() +
      "/versions/" +
      (await currentEnvironment(context)).version_id,
  );
  expect(version.variables.UI_MARKER).toBeUndefined();
});
test("UI creates a Worker and a preview environment", async ({ page }) => {
  await page.goto(locationFor());
  await page.getByRole("button", { name: "创建 Worker", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("textbox", { name: "Worker 名称", exact: true })
    .fill("UI created " + run);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "创建 Worker", exact: true })
    .click();
  await expect(
    page.getByRole("tab", { name: "配置版本", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "创建预览环境", exact: true }).click();
  await page.getByRole("dialog").getByRole("textbox").fill("ui-preview");
  await page.getByRole("dialog").getByRole("button", { name: /^创建/ }).click();
  await expect(
    page.getByRole("button", { name: /ui-preview/ }).first(),
  ).toBeVisible();
});
test("mobile navigation and Workers layout remain usable", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(locationFor());
  await expect(
    page.getByRole("button", { name: "创建 Worker", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "关闭导航", exact: true }),
  ).toBeVisible();
  // The backdrop spans the viewport; its center lies under the drawer.
  // Tap the exposed right edge, as a mobile user dismissing the drawer does.
  await page
    .getByRole("button", { name: "关闭导航", exact: true })
    .click({ position: { x: 370, y: 120 } });
  await page.screenshot({
    path: ".data/performance-ui-mobile.png",
    fullPage: false,
  });
});

test("UI saves an undeployed Version and lists pinned deployments", async ({
  page,
  context,
}) => {
  await context.addCookies(cookies);
  await page.goto(locationFor(workflow.id));
  await page.getByRole("tab", { name: "构建", exact: true }).click();
  const before = await currentEnvironment(context);
  const upload = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      r.url().endsWith("/environments/production/versions"),
  );
  await page.getByRole("button", { name: "更多构建操作", exact: true }).first().click();
  await page.getByRole("menuitem", { name: "创建待部署版本", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "创建待部署版本", exact: true });
  await dialog.getByRole("textbox", { name: "版本标签（可选）" }).fill("ui-candidate-" + run);
  await dialog.getByRole("button", { name: "创建待部署版本", exact: true }).click();
  const response = await upload;
  expect(response.status()).toBe(201);
  const version = await response.json();
  await expect
    .poll(async () =>
      Object.values(
        (await api(context, workerBase() + "/overview")).versions,
      ).some((v) => v.id === version.id),
    )
    .toBe(true);
  expect((await currentEnvironment(context)).version_id).toBe(
    before.version_id,
  );
  await expect(page.getByRole("tab", { name: "版本与流量", exact: true })).toHaveAttribute("data-state", "active");
  await expect(page.getByText("ui-candidate-" + run, { exact: true })).toBeVisible();
  await expect(
    page.getByText(version.id.slice(0, 16), { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(page.getByText("ui-candidate-" + run, { exact: true })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "部署记录与固定地址" }),
  ).toBeVisible();
  await expect(page.locator('a[href*="-d-"]').first()).toBeVisible();
  await page.screenshot({
    path: ".data/cf-model-ui-versions.png",
    fullPage: true,
  });
});

test("UI configures Previews Base without changing production", async ({
  page,
  context,
}) => {
  await context.addCookies(cookies);
  await page.goto(locationFor(workflow.id));
  await page.getByRole("button", { name: "创建预览环境", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByText("新建预览默认配置", { exact: true }).click();
  const production = await currentEnvironment(context);
  await fillVariables(page, {
    GREETING: "base-from-ui",
    CONFIG: { nested: [1, true, null] },
  });
  await dialog
    .getByRole("button", { name: "保存环境变量", exact: true })
    .click();
  await expect(
    page.getByText("新建预览默认配置已保存，仅影响之后新建的预览。", {
      exact: true,
    }),
  ).toBeVisible();
  await dialog
    .getByRole("textbox", { name: "预览名称", exact: true })
    .fill("base-" + run);
  await dialog.getByRole("button", { name: "创建", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const preview = (await api(context, workerBase() + "/environments")).items.find(
    (environment) => environment.name === "base-" + run,
  );
  expect(preview.variables.GREETING).toBe("base-from-ui");
  expect(preview.variables.CONFIG).toEqual({ nested: [1, true, null] });
  expect((await currentEnvironment(context)).version_id).toBe(
    production.version_id,
  );
});


test("UI edits JSON values and metadata bindings, then displays immutable version tags", async ({
  page,
  context,
}) => {
  await context.addCookies(cookies);
  await page.goto(locationFor(workflow.id));
  await page.getByRole("tab", { name: "配置版本", exact: true }).click();
  await page.getByText("管理环境草稿", { exact: true }).click();
  await fillVariables(page, { CONFIG: { nested: [1, true, null] }, COUNT: 7, FLAG: false });
  await page.getByText("高级设置：在代码中读取版本信息", { exact: true }).click();
  await page
    .getByRole("textbox", { name: "版本信息变量名", exact: true })
    .fill("CF_VERSION_METADATA");
  await page.getByRole("button", { name: "保存环境变量", exact: true }).click();
  await expect
    .poll(async () => (await currentEnvironment(context)).variables.CONFIG)
    .toEqual({ nested: [1, true, null] });
  expect((await currentEnvironment(context)).version_metadata).toBe("CF_VERSION_METADATA");
  const overview = await api(context, workerBase() + "/overview");
  const build = Object.values(overview.builds).find((b) => b.state === "succeeded");
  expect(build).toBeTruthy();
  const version = await api(context, workerBase() + "/environments/production/versions", "POST", {
    build_id: build.id,
    tag: "ui-json-v1",
    message: "JSON configuration snapshot",
    idempotency_key: randomUUID(),
  });
  expect(version.variables.COUNT).toBe(7);
  expect(version.variables.FLAG).toBe(false);
  expect(version.version_metadata).toBe("CF_VERSION_METADATA");
  await page.goto(locationFor(workflow.id));
  await page.getByRole("tab", { name: "版本与流量", exact: true }).click();
  await expect(page.getByText("ui-json-v1", { exact: true })).toBeVisible();
  await expect(page.getByText("JSON configuration snapshot", { exact: true })).toBeVisible();
  await page.screenshot({ path: ".data/bindings/ui-json-version.png", fullPage: true });
});
