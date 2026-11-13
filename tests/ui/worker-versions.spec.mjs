import { test, expect } from "@playwright/test";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { resolve, extname } from "node:path";

// Use the built static SPA at one origin. API fixtures cover UI feedback and
// races; workers_versions_test.go covers snapshot creation and deployment.
let server;
let origin;
const workerBase = "/api/v1/orgs/org-versions/workers/worker-versions";
const location = "/?page=workers&workspace=org-versions&worker=worker-versions";

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

async function fixture(page) {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));
  const environment = (name) => ({
    name,
    enabled: true,
    domains: [],
    hosts: [],
    configuration_revision: 4,
    deployment_revision: 3,
    variables: { MODE: "reviewed" },
    secret_names: ["API_TOKEN"],
    r2_buckets: { FILES: { bucket_id: "bucket-files", bucket_name: "files" } },
  });
  const production = {
    ...environment("production"),
    version_id: "version-current",
    confirmed_version_id: "version-current",
  };
  const state = {
    environments: [production, environment("preview")],
    versions: {
      "version-current": {
        id: "version-current",
        environment: "production",
        artifact_id: "artifact-live",
        variables: { MODE: "live" },
        configuration_revision: 1,
        created_at: "2026-01-01T00:00:00Z",
        created_by: "user-versions",
        created_by_name: "Version tester",
      },
    },
    builds: [
      {
        id: "build-candidate",
        state: "succeeded",
        environment: "production",
        artifact_id: "artifact-candidate",
        entrypoint: "index.ts",
        created_at: "2026-01-03T00:00:00Z",
        repository_source: {
          repository: "example/worker",
          ref: "feature/candidate",
          commit: "abc123abc123000",
        },
      },
      {
        id: "build-live",
        state: "succeeded",
        environment: "production",
        artifact_id: "artifact-live",
        entrypoint: "index.ts",
        created_at: "2026-01-02T00:00:00Z",
      },
      {
        id: "build-failed",
        state: "failed",
        environment: "production",
        entrypoint: "index.ts",
        created_at: "2026-01-01T00:00:00Z",
      },
    ],
    configurations: [],
    operations: [],
    deployments: [],
    listRequests: [],
    waitForDeploy: null,
    creates: [],
    deploys: [],
    failCreate: false,
    failDeploy: false,
    omitCreatedFromOverview: false,
    waitForCreate: null,
    repository: null,
  };
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data = { items: [] };
    let status = 200;
    if (path === "/api/auth/me") {
      data = {
        user: {
          user_id: "user-versions",
          name: "Version tester",
          source_id: "test",
          status: "active",
        },
        system_admin: false,
        workers_configured: true,
        revision: 1,
      };
    } else if (path === "/api/auth/providers") {
      data = { providers: [], development: false };
    } else if (path === "/api/auth/csrf") {
      data = { csrf_token: "test-csrf" };
    } else if (path === "/api/v1/orgs") {
      data = {
        items: [{ id: "org-versions", name: "Version workflow", allowed_sources: [], revision: 1 }],
      };
    } else if (path.endsWith("/permissions")) {
      data = {
        organization_id: "org-versions",
        actions: { "organization.read": true, "worker.list": true, "worker.create": true },
        revision: 1,
      };
    } else if (path.endsWith("/network-profiles")) {
      data = { items: [] };
    } else if (path === "/api/v1/orgs/org-versions/workers") {
      data = {
        items: [
          {
            id: "worker-versions",
            name: "Version workflow",
            created_at: "2026-01-01T00:00:00Z",
            network_profile: {},
            environments: state.environments,
          },
        ],
      };
    } else if (path === workerBase + "/overview") {
      data = {
        builds: state.builds,
        versions: state.omitCreatedFromOverview
          ? { "version-current": state.versions["version-current"] }
          : state.versions,
        environments: state.environments.map((environment) => ({ environment, history: [] })),
        operations: state.operations,
        domains_revision: 1,
      };
    } else if (path === workerBase + "/repository") {
      data = { source: state.repository, revision: 1 };
    } else if (path.endsWith("/workers/repository-providers")) {
      data = { items: [] };
    } else if (path === workerBase + "/domains") {
      data = { selected: [], catalog: [], revision: 1, network_profile: {} };
    } else if (
      (path === workerBase + "/versions" || path === workerBase + "/deployments") &&
      request.method() === "GET"
    ) {
      const query = new URL(request.url()).searchParams;
      state.listRequests.push({ path, query: Object.fromEntries(query) });
      const source = path.endsWith("/versions")
        ? Object.values(state.versions)
        : state.deployments.map((deployment) => ({
            ...deployment,
            state:
              state.operations.find((operation) => operation.id === deployment.operation_id)
                ?.state || deployment.state,
          }));
      const search = (query.get("q") || "").toLowerCase();
      const items = source
        .filter(
          (item) =>
            item.environment === query.get("environment") &&
            JSON.stringify(item).toLowerCase().includes(search),
        )
        .sort((a, b) => b.created_at.localeCompare(a.created_at) || a.id.localeCompare(b.id));
      const after = query.get("after");
      const offset = after ? items.findIndex((item) => item.id === after) + 1 : 0;
      const limit = Number(query.get("limit") || 100);
      data = {
        items: items.slice(offset, offset + limit),
        next_after: items.length > offset + limit ? items[offset + limit - 1].id : "",
      };
    } else if (path.startsWith(workerBase + "/versions/") && request.method() === "GET") {
      data = state.versions[decodeURIComponent(path.slice((workerBase + "/versions/").length))];
      if (!data) {
        status = 404;
        data = { error: { code: "RESOURCE_NOT_FOUND", message: "Version not found" } };
      }
    } else if (path.endsWith("/configurations") && request.method() === "GET") {
      data = { items: state.configurations };
    } else if (
      path.includes("/configurations/") &&
      path.endsWith("/deployments") &&
      request.method() === "POST"
    ) {
      const input = request.postDataJSON();
      state.deploys.push(input);
      const env = state.environments[0];
      if (
        input.expected_revision !== env.deployment_revision ||
        input.expected_configuration_revision !== env.configuration_revision
      ) {
        status = 409;
        data = {
          error: {
            code: "REVISION_CONFLICT",
            message: "Environment configuration or deployment changed",
          },
        };
      } else {
        const configuration = state.configurations.find((item) =>
          path.includes("/" + item.id + "/"),
        );
        state.versions["configuration-release"] = {
          ...state.versions[input.code_version_id],
          id: "configuration-release",
          configuration_id: configuration.id,
          variables: configuration.variables,
          tag: configuration.name,
        };
        data = {
          id: "configuration-operation",
          kind: "publish",
          environment: env.name,
          version_id: "configuration-release",
          state: "queued",
          created_at: "2026-01-04T01:00:00Z",
          steps: [{ name: "candidate", state: "pending" }],
        };
        state.operations.unshift(data);
        status = 202;
      }
    } else if (path.endsWith("/versions") && request.method() === "POST") {
      const input = request.postDataJSON();
      state.creates.push({ path, input });
      if (state.waitForCreate) await state.waitForCreate;
      const env = state.environments.find((item) =>
        path.endsWith(`/environments/${item.name}/versions`),
      );
      if (input.expected_configuration_revision !== env.configuration_revision) {
        status = 409;
        data = {
          error: { code: "REVISION_CONFLICT", message: "Environment configuration changed" },
        };
      } else if (state.failCreate) {
        status = 503;
        data = { error: { code: "UNAVAILABLE", message: "Try again" } };
      } else {
        status = 201;
        data = {
          id: "version-candidate",
          environment: env.name,
          artifact_id: state.builds.find((build) => build.id === input.build_id).artifact_id,
          variables: structuredClone(env.variables),
          configuration_revision: env.configuration_revision,
          r2_buckets: env.r2_buckets,
          secret_digest: "frozen-secret-digest",
          tag: input.tag,
          message: input.message,
          created_at: "2026-01-04T00:00:00Z",
          created_by: "user-versions",
          created_by_name: "Version tester",
        };
        state.versions[data.id] = data;
      }
    } else if (path.endsWith("/deployments") && request.method() === "POST") {
      const input = request.postDataJSON();
      state.deploys.push(input);
      if (state.waitForDeploy) await state.waitForDeploy;
      if (state.failDeploy) {
        status = 409;
        data = { error: { code: "REVISION_CONFLICT", message: "Environment version changed" } };
      } else {
        status = 202;
        data = {
          id: "op-candidate",
          kind: "publish",
          environment: "production",
          version_id: input.version_id,
          state: "queued",
          created_at: "2026-01-04T01:00:00Z",
          steps: [{ name: "artifact", state: "pending" }],
        };
        state.operations.unshift(data);
        state.deployments.unshift({
          id: "dep-candidate",
          environment: "production",
          version_id: input.version_id || "version-current",
          operation_id: data.id,
          state: "queued",
          hosts: [],
          created_at: data.created_at,
          expires_at: "2026-02-04T01:00:00Z",
        });
      }
    }
    await route.fulfill({ status, json: data });
  });
  return { state, errors };
}

async function openCreate(page) {
  await page.getByRole("button", { name: "更多构建操作", exact: true }).first().click();
  await page.getByRole("menuitem", { name: "创建待部署版本", exact: true }).click();
  return page.getByRole("dialog", { name: "创建待部署版本", exact: true });
}

test("create, find after refresh, inspect and deploy a frozen version", async ({ page }) => {
  const { state, errors } = await fixture(page);
  state.omitCreatedFromOverview = true;
  await page.goto(origin + location);
  await expect(page.getByRole("button", { name: "保存版本", exact: true })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "更多构建操作", exact: true }).last(),
  ).toBeDisabled();
  const dialog = await openCreate(page);
  await expect(dialog).toContainText("production");
  await expect(dialog).toContainText("feature/candidate");
  await expect(dialog).toContainText("API_TOKEN");
  await expect(dialog).toContainText("FILES → files");
  await dialog.getByText("普通变量（1）", { exact: true }).click();
  await expect(dialog.locator("pre")).toContainText('"MODE": "reviewed"');
  await dialog.getByRole("textbox", { name: "版本标签（可选）" }).fill("release-candidate");
  await dialog.getByRole("textbox", { name: "版本说明（可选）" }).fill("Prepare now, deploy later");
  await page.screenshot({ path: ".data/version-flow/create-desktop.png", fullPage: false });

  let release;
  state.waitForCreate = new Promise((resolve) => {
    release = resolve;
  });
  await dialog.getByRole("button", { name: "创建待部署版本", exact: true }).click();
  await expect(dialog.getByRole("button", { name: "创建中…", exact: true })).toBeDisabled();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeVisible();
  await expect.poll(() => state.creates.length).toBe(1);
  release();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("tab", { name: "版本与部署", exact: true })).toHaveAttribute(
    "data-state",
    "active",
  );
  const candidate = page.locator('[aria-label="版本 version-candidate"]');
  await expect(candidate).toBeVisible();
  await expect(candidate).toBeFocused();
  await expect(candidate).toContainText("待部署版本已创建，线上部署未改变。");
  await expect(candidate).toContainText("release-candidate");
  await expect(candidate).toContainText("build-candidate");
  await expect(candidate).toContainText("待部署");
  await expect(candidate.locator("time")).toHaveAttribute("datetime", "2026-01-04T00:00:00Z");
  expect(state.environments[0].version_id).toBe("version-current");
  expect(state.deploys).toHaveLength(0);
  expect(state.creates[0].input).toMatchObject({
    build_id: "build-candidate",
    expected_configuration_revision: 4,
    tag: "release-candidate",
    message: "Prepare now, deploy later",
  });

  state.omitCreatedFromOverview = false;
  state.environments[0].variables.MODE = "changed-after-creation";
  state.environments[0].configuration_revision = 5;
  await page.reload();
  await expect(page.getByRole("tab", { name: "版本与部署", exact: true })).toHaveAttribute(
    "data-state",
    "active",
  );
  await expect(candidate).toBeVisible();
  await candidate.getByText("查看冻结配置", { exact: false }).click();
  await candidate.getByText("普通变量（1）", { exact: true }).click();
  await expect(candidate.locator("pre")).toContainText('"MODE": "reviewed"');
  await expect(candidate).not.toContainText("changed-after-creation");
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await candidate.getByRole("button", { name: "部署此版本" }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: ".data/version-flow/versions-mobile.png", fullPage: true });
  await expect(candidate.getByRole("button", { name: "部署此版本" })).toBeInViewport();
  await page.setViewportSize({ width: 1440, height: 1000 });
  await candidate.getByRole("button", { name: "部署此版本" }).click();
  const confirm = page.getByRole("dialog", { name: "部署已保存版本" });
  await expect(confirm).toContainText("production");
  await confirm.getByText("普通变量（1）", { exact: true }).click();
  await expect(confirm.locator("pre")).toContainText('"MODE": "reviewed"');
  expect(state.deploys).toHaveLength(0);
  await confirm.getByRole("button", { name: "确认部署" }).click();
  await expect(confirm).toHaveCount(0);
  expect(state.deploys).toHaveLength(1);
  expect(state.deploys[0]).toMatchObject({ version_id: "version-candidate", expected_revision: 3 });
  expect(state.deploys[0].build_id).toBeUndefined();
  await expect(candidate).toContainText("部署中");
  await expect(page.getByText("op-candidate · publish", { exact: true })).toBeVisible();
  await expect(page.getByText("待部署版本已创建，线上部署未改变。", { exact: true })).toHaveCount(
    0,
  );
  await page.screenshot({ path: ".data/version-flow/deployment-desktop.png", fullPage: true });
  expect(errors).toEqual([]);
});

test("configuration changes block creation until reviewed again in the same environment", async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  await page.goto(origin + location);
  await page
    .getByRole("button", { name: /preview/ })
    .first()
    .click();
  const dialog = await openCreate(page);
  await expect(dialog).toContainText("preview");
  state.environments[1].configuration_revision++;
  state.environments[1].variables.MODE = "updated-preview";
  await dialog.getByRole("button", { name: "创建待部署版本", exact: true }).click();
  await expect(dialog.getByRole("alert")).toHaveText(
    "环境配置已改变，请关闭后重新创建并核对配置。",
  );
  await expect(dialog.getByRole("button", { name: "创建待部署版本", exact: true })).toBeDisabled();
  expect(state.versions["version-candidate"]).toBeUndefined();
  await Promise.all([
    page.waitForResponse((response) => response.url().endsWith("/overview")),
    dialog.getByRole("button", { name: "取消", exact: true }).click(),
  ]);
  const refreshed = await openCreate(page);
  await refreshed.getByText("普通变量（1）", { exact: true }).click();
  await expect(refreshed.locator("pre")).toContainText("updated-preview");
  await refreshed.getByRole("button", { name: "创建待部署版本", exact: true }).click();
  const candidate = page.locator('[aria-label="版本 version-candidate"]');
  await expect(candidate).toBeVisible();
  expect(state.creates[1].path).toBe(workerBase + "/environments/preview/versions");
  expect(state.creates[1].input.expected_configuration_revision).toBe(5);
  await expect(page.locator('[aria-label="版本 version-current"]')).toHaveCount(0);
  await page.reload();
  await expect(candidate).toBeVisible();
  await expect(page.getByText("preview · 版本列表", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("a failed request can be retried without creating a new idempotency key", async ({ page }) => {
  const { state, errors } = await fixture(page);
  state.failCreate = true;
  await page.goto(origin + location);
  const dialog = await openCreate(page);
  await dialog.getByRole("button", { name: "创建待部署版本", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("Try again");
  await expect(dialog.getByRole("button", { name: "创建待部署版本", exact: true })).toBeEnabled();
  state.failCreate = false;
  await dialog.getByRole("button", { name: "创建待部署版本", exact: true }).click();
  const candidate = page.locator('[aria-label="版本 version-candidate"]');
  await expect(candidate).toBeVisible();
  expect(state.creates).toHaveLength(2);
  expect(state.creates[1].input.idempotency_key).toBe(state.creates[0].input.idempotency_key);
  state.failDeploy = true;
  await candidate.getByRole("button", { name: "部署此版本" }).click();
  const confirm = page.getByRole("dialog", { name: "部署已保存版本" });
  await confirm.getByRole("button", { name: "确认部署" }).click();
  await expect(confirm.getByRole("alert")).toBeVisible();
  await expect(confirm.getByRole("button", { name: "确认部署" })).toBeDisabled();
  await confirm.getByRole("button", { name: "取消", exact: true }).click();
  expect(state.environments[0].version_id).toBe("version-current");
  expect(errors).toEqual([]);
});

test("project deployment keeps local upload available alongside Git builds", async ({ page }) => {
  const { state } = await fixture(page);
  await page.goto(origin + location);
  const deployment = page.getByRole("region", { name: "部署项目", exact: true });
  const git = deployment.getByRole("region", { name: "Git 仓库", exact: true });
  const upload = deployment.getByRole("region", { name: "上传部署", exact: true });
  await expect(upload.getByRole("button", { name: "选择 ZIP" })).toBeEnabled();
  await git.getByRole("button", { name: "绑定代码仓库" }).click();
  await expect(page.getByText(/管理员尚未配置 Git 仓库来源/)).toBeVisible();
  await page.getByRole("tab", { name: "构建与发布", exact: true }).click();
  await expect(upload.getByRole("button", { name: "选择文件夹" })).toBeEnabled();

  state.repository = {
    provider_id: "github",
    repository: "example/worker",
    ref: "main",
    root_directory: ".",
  };
  await page.reload();
  await expect(upload.getByRole("button", { name: "选择 ZIP" })).toBeEnabled();
  await git.getByRole("button", { name: "从仓库构建" }).click();
  await expect(page.getByRole("dialog", { name: "从仓库构建" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "分支 / Tag / Commit" })).toHaveValue("main");
  await page.keyboard.press("Escape");

  await page.setViewportSize({ width: 430, height: 900 });
  await expect
    .poll(() => deployment.evaluate((el) => Math.ceil(el.getBoundingClientRect().right)), {
      timeout: 3000,
    })
    .toBeLessThanOrEqual(430);
});

test("direct publishing stays available without creating an undeployed version first", async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  await page.goto(origin + location + "&worker_tab=versions&environment=preview");
  await expect(
    page.getByText("此环境还没有版本。可以直接发布成功构建，或先创建待部署版本。", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: /production/ })
    .first()
    .click();
  await page.getByRole("tab", { name: "构建与发布", exact: true }).click();
  const draft = await openCreate(page);
  await draft.getByRole("button", { name: "取消", exact: true }).click();
  expect(state.creates).toHaveLength(0);
  await page.getByRole("button", { name: "发布", exact: true }).first().click();
  const confirm = page.getByRole("dialog", { name: "发布构建版本", exact: true });
  await confirm.getByRole("button", { name: "确认发布", exact: true }).click();
  await expect(confirm).toHaveCount(0);
  expect(state.creates).toHaveLength(0);
  expect(state.deploys).toHaveLength(1);
  expect(state.deploys[0]).toMatchObject({ build_id: "build-candidate", expected_revision: 3 });
  expect(state.deploys[0].version_id).toBeUndefined();
  expect(errors).toEqual([]);
});

test("versions and deployments paginate independently and open version details outside the current page", async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  for (let i = 0; i < 25; i++) {
    const id = `version-page-${String(i).padStart(3, "0")}`;
    state.versions[id] = {
      ...state.versions["version-current"],
      id,
      tag: `release-${i}`,
      message: `Release message ${i}`,
      created_by: "author-123",
      created_by_name: "Release Author",
      created_at: "2026-01-05T00:00:00Z",
    };
    state.deployments.push({
      id: `deployment-${String(i).padStart(3, "0")}`,
      version_id: id,
      environment: "production",
      operation_id: `operation-${i}`,
      state: "available",
      hosts: [],
      created_at: "2026-01-05T01:00:00Z",
      expires_at: "2026-02-05T01:00:00Z",
    });
  }
  await page.goto(origin + location + "&worker_tab=versions");
  const versions = page.getByRole("region", { name: "版本列表", exact: true });
  const deployments = page.getByRole("region", { name: "部署记录与固定地址", exact: true });
  await expect(versions.locator('[aria-label^="版本 version-"]')).toHaveCount(20);
  await expect(versions).toContainText("Release Author");
  await expect(deployments.getByRole("button", { name: /^version-page-/ })).toHaveCount(20);
  await versions.getByRole("button", { name: "下一页", exact: true }).click();
  await expect(versions).toContainText("第 2 页");
  await expect(versions.locator('[aria-label^="版本 version-"]')).toHaveCount(6);
  await expect(deployments).toContainText("第 1 页");
  await deployments.getByRole("button", { name: "下一页", exact: true }).click();
  await expect(deployments.getByRole("button", { name: /^version-page-/ })).toHaveCount(5);
  await expect(deployments.getByRole("button", { name: "下一页", exact: true })).toBeDisabled();
  await deployments.getByRole("button", { name: "version-page-024", exact: true }).click();
  const detail = page.getByRole("dialog", { name: "版本详情", exact: true });
  await expect(detail).toContainText("Release Author");
  await expect(detail).toContainText("author-123");
  await expect(detail).toContainText("Release message 24");
  await expect(detail.locator("time")).toHaveAttribute("datetime", "2026-01-05T00:00:00Z");
  await expect(detail).toContainText("查看冻结配置");
  await page.keyboard.press("Escape");
  await versions.getByRole("button", { name: "上一页", exact: true }).click();
  await expect(versions.locator('[aria-label^="版本 version-"]')).toHaveCount(20);
  await expect(versions.locator('[aria-label="版本 version-current"]')).toHaveCount(0);
  const current = page.getByRole("region", { name: "当前部署", exact: true });
  await current.getByRole("button", { name: "version-current", exact: true }).click();
  await expect(detail).toContainText("当前生效版本");
  await expect(detail).toContainText("Version tester");
  await page.keyboard.press("Escape");
  await versions
    .getByRole("textbox", { name: "搜索版本 ID、标签、说明或保存者" })
    .fill("release-24");
  await versions.getByRole("button", { name: "搜索", exact: true }).click();
  await expect(versions.locator('[aria-label^="版本 version-"]')).toHaveCount(1);
  await expect(versions).toContainText("第 1 页");
  await expect(deployments).toContainText("第 2 页");
  await versions.getByRole("button", { name: "清除", exact: true }).click();
  await versions.getByRole("combobox", { name: "每页条数", exact: true }).selectOption("50");
  await expect(versions.locator('[aria-label^="版本 version-"]')).toHaveCount(26);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  expect(state.listRequests.some(({ query }) => query.limit === "20" && query.after)).toBeTruthy();
  expect(errors).toEqual([]);
});

test("deployment stays visible while submitting, then reports progress, failure and confirmed success", async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  state.versions["version-candidate"] = {
    ...state.versions["version-current"],
    id: "version-candidate",
    created_at: "2026-01-05T00:00:00Z",
  };
  await page.goto(origin + location + "&worker_tab=versions");
  const candidate = page.locator('[aria-label="版本 version-candidate"]');
  await candidate.getByRole("button", { name: "部署此版本", exact: true }).click();
  const confirm = page.getByRole("dialog", { name: "部署已保存版本" });
  let release;
  state.waitForDeploy = new Promise((resolve) => {
    release = resolve;
  });
  await confirm.getByRole("button", { name: "确认部署", exact: true }).click();
  await expect(confirm.getByRole("button", { name: "提交中…", exact: true })).toBeDisabled();
  await page.keyboard.press("Escape");
  await expect(confirm).toBeVisible();
  expect(state.deploys).toHaveLength(1);
  release();
  await expect(confirm).toHaveCount(0);
  await expect(
    page.getByText("部署请求已受理，正在排队或验证，请等待生效。", { exact: true }),
  ).toBeVisible();
  await expect(candidate.getByRole("button", { name: "部署此版本", exact: true })).toBeDisabled();
  const operation = state.operations[0];
  operation.state = "failed";
  operation.error = "Candidate validation failed: missing binding";
  await expect(page.getByText("部署失败，请查看下方错误详情。", { exact: true })).toBeVisible();
  await expect(page.getByRole("alert")).toContainText(operation.error);
  await expect(candidate.getByRole("button", { name: "部署此版本", exact: true })).toBeEnabled();
  await expect(page.getByRole("region", { name: "当前部署", exact: true })).toContainText(
    "version-current",
  );
  // Exercise the committed -> runtime acknowledgement transition independently
  // from HTTP acceptance, which must never be presented as success.
  operation.state = "committed";
  delete operation.error;
  state.environments[0].version_id = "version-candidate";
  await page.reload();
  await expect(
    page.getByText("部署已提交，正在等待运行副本确认生效。", { exact: true }),
  ).toBeVisible();
  const current = page.getByRole("region", { name: "当前部署", exact: true });
  await expect(current).toContainText("正在生效的版本");
  await expect(current.getByRole("button", { name: "version-current", exact: true })).toBeVisible();
  operation.state = "available";
  state.environments[0].confirmed_version_id = "version-candidate";
  await expect(page.getByText("部署成功，运行副本已确认生效。", { exact: true })).toBeVisible();
  await expect(
    current.getByRole("button", { name: "version-candidate", exact: true }),
  ).toBeVisible();
  await expect(current.getByRole("button", { name: "version-current", exact: true })).toHaveCount(
    0,
  );
  expect(errors).toEqual([]);
});

test("configuration publication preserves code and updates the active marker only after confirmation", async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  state.configurations = [
    {
      id: "config-candidate",
      name: "XXX 测试",
      environment: "production",
      variables: { MODE: "candidate" },
      secret_names: [],
      created_at: "2026-01-02T00:00:00Z",
    },
    {
      id: "config-live",
      name: "正式基线",
      environment: "production",
      variables: { MODE: "live" },
      secret_names: [],
      created_at: "2026-01-01T00:00:00Z",
    },
  ];
  state.versions["version-current"].configuration_id = "config-live";
  await page.goto(origin + location + "&worker_tab=configurations");
  const active = page
    .getByRole("tabpanel", { name: "配置版本" })
    .locator('[data-slot="card"]')
    .first();
  await expect(active).toContainText("正式基线");
  const detail = page.getByRole("region", { name: "配置版本详情" });
  await detail.getByRole("button", { name: "发布并生效", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "确认发布到生产环境", exact: true });
  await expect(dialog).toContainText("version-current");
  await expect(dialog).toContainText("XXX 测试");
  await dialog.getByRole("button", { name: "确认发布", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(state.deploys[0].code_version_id).toBe("version-current");
  expect(state.deploys[0].expected_configuration_revision).toBe(4);
  expect(state.versions["configuration-release"].artifact_id).toBe("artifact-live");
  await expect(active).toContainText("正式基线");
  state.environments[0].version_id = "configuration-release";
  state.operations[0].state = "committed";
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  await expect(page.getByRole("heading", { name: /发布进度/ })).toBeVisible();
  await expect(active).toContainText("正式基线");
  state.environments[0].confirmed_version_id = "configuration-release";
  state.environments[0].deployment_revision++;
  state.operations[0].state = "available";
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  await expect(active).toContainText("XXX 测试");
  await expect(detail.getByRole("button", { name: "当前已生效", exact: true })).toBeDisabled();
  expect(errors).toEqual([]);
});

test("deploys a saved version at a fractional canary percentage", async ({ page }) => {
  const { state } = await fixture(page);
  state.versions["version-canary"] = {
    ...state.versions["version-current"],
    id: "version-canary",
    tag: "canary",
  };
  await page.goto(origin + location);
  await page.getByRole("tab", { name: "版本与部署", exact: true }).click();
  await page
    .getByLabel("版本 version-canary", { exact: true })
    .getByRole("button", { name: "部署此版本" })
    .click();
  const dialog = page.getByRole("dialog", { name: "部署已保存版本" });
  await dialog.getByLabel("此版本流量百分比").fill("5.125");
  await dialog.getByRole("button", { name: "确认部署" }).click();
  await expect(dialog).toHaveCount(0);
  expect(state.deploys).toHaveLength(1);
  expect(state.deploys[0]).toMatchObject({
    expected_revision: 3,
    versions: [
      { version_id: "version-current", percentage: 94.875 },
      { version_id: "version-canary", percentage: 5.125 },
    ],
  });
});
