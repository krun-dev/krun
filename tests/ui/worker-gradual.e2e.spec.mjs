import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

const run = randomUUID().slice(0, 8);

async function api(context, path, method = "GET", data) {
  const headers = {};
  if (method !== "GET") {
    headers.origin = new URL(process.env.KRUN_UI_ORIGIN || "http://127.0.0.1:4380").origin;
    const csrf = await context.request.post("/api/auth/csrf", { headers });
    expect(csrf.ok()).toBeTruthy();
    headers["X-CSRF-Token"] = (await csrf.json()).csrf_token;
  }
  const response = await context.request.fetch(path, { method, headers, data });
  expect(response.ok(), method + " " + path + ": " + (await response.text())).toBeTruthy();
  return response.json();
}

async function waitForRelease(context, workerBase, operation) {
  await expect
    .poll(async () => (await api(context, workerBase + "/operations/" + operation)).state, {
      timeout: 150_000,
      intervals: [1000, 2000],
    })
    .toBe("available");
}

test("UI publishes a saved version at a fractional percentage on the real runtime", async ({
  page,
  context,
}) => {
  await page.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));
  await page.goto("/?page=login");
  await page.getByRole("button", { name: /本地开发登录|kind 开发登录/ }).click();
  await expect(page.getByRole("button", { name: "退出登录" })).toBeVisible();

  const org = await api(context, "/api/v1/orgs", "POST", {
    name: "Gradual console " + run,
  });
  const orgBase = "/api/v1/orgs/" + org.id;
  const worker = await api(context, orgBase + "/workers", "POST", { name: "canary-" + run });
  const workerBase = orgBase + "/workers/" + worker.id;
  const environment = workerBase + "/environments/production";
  const current = async () =>
    (await api(context, workerBase + "/environments")).items.find(
      (item) => item.name === "production",
    );
  const build = await api(context, workerBase + "/builds", "POST", {
    environment: "production",
    entrypoint: "index.js",
    files: { "index.js": "export default { fetch() { return new Response('canary'); } }" },
    idempotency_key: randomUUID(),
  });
  await expect
    .poll(async () => (await api(context, workerBase + "/builds/" + build.id)).state, {
      timeout: 150_000,
      intervals: [1000, 2000],
    })
    .toBe("succeeded");
  const stablePublish = await api(context, environment + "/deployments", "POST", {
    build_id: build.id,
    expected_revision: 0,
    idempotency_key: randomUUID(),
  });
  await waitForRelease(context, workerBase, stablePublish.id);
  const stable = await current();
  const candidate = await api(context, environment + "/versions", "POST", {
    build_id: build.id,
    tag: "fractional-" + run,
    idempotency_key: randomUUID(),
  });
  await page.goto("/?page=workers&workspace=" + org.id + "&worker=" + worker.id);
  await page.getByRole("tab", { name: "版本与部署", exact: true }).click();
  await page
    .getByLabel("版本 " + candidate.id, { exact: true })
    .getByRole("button", { name: "部署此版本", exact: true })
    .click();
  const dialog = page.getByRole("dialog", { name: "部署已保存版本" });
  await dialog.getByLabel("此版本流量百分比").fill("5.125");
  const submitted = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/environments/production/deployments"),
  );
  await dialog.getByRole("button", { name: "确认部署", exact: true }).click();
  const response = await submitted;
  expect(response.status()).toBe(202);
  const operation = await response.json();
  await waitForRelease(context, workerBase, operation.id);
  expect(await current()).toMatchObject({
    version_id: stable.version_id,
    candidate_version: candidate.id,
    candidate_weight: 5125,
    deployment_revision: stable.deployment_revision + 1,
  });
  await expect(page.getByRole("region", { name: "当前部署" })).toContainText("5.125%");
});
