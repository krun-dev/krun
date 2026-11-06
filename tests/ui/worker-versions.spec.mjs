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
    environments: [production, environment("staging")],
    versions: {
      "version-current": {
        id: "version-current",
        environment: "production",
        artifact_id: "artifact-live",
        variables: { MODE: "live" },
        configuration_revision: 1,
        created_at: "2026-01-01T00:00:00Z",
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
    operations: [],
    creates: [],
    deploys: [],
    failCreate: false,
    failDeploy: false,
    omitCreatedFromOverview: false,
    waitForCreate: null,
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
      data = { source: null, revision: 1 };
    } else if (path === workerBase + "/domains") {
      data = { selected: [], catalog: [], revision: 1, network_profile: {} };
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
        };
        state.versions[data.id] = data;
      }
    } else if (path.endsWith("/deployments") && request.method() === "POST") {
      const input = request.postDataJSON();
      state.deploys.push(input);
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
  expect(state.creates).toHaveLength(1);
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
    .getByRole("button", { name: /staging/ })
    .first()
    .click();
  const dialog = await openCreate(page);
  await expect(dialog).toContainText("staging");
  state.environments[1].configuration_revision++;
  state.environments[1].variables.MODE = "updated-staging";
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
  await expect(refreshed.locator("pre")).toContainText("updated-staging");
  await refreshed.getByRole("button", { name: "创建待部署版本", exact: true }).click();
  const candidate = page.locator('[aria-label="版本 version-candidate"]');
  await expect(candidate).toBeVisible();
  expect(state.creates[1].path).toBe(workerBase + "/environments/staging/versions");
  expect(state.creates[1].input.expected_configuration_revision).toBe(5);
  await expect(page.locator('[aria-label="版本 version-current"]')).toHaveCount(0);
  await page.reload();
  await expect(candidate).toBeVisible();
  await expect(page.getByText("staging · 版本列表", { exact: true })).toBeVisible();
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

test("direct publishing stays available without creating an undeployed version first", async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  await page.goto(origin + location + "&worker_tab=versions&environment=staging");
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
