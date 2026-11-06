import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

test.use({ actionTimeout: 20_000 });

// Real Go/PostgreSQL/MinIO fixture; both the static console and API are served
// from the same origin. This test needs no running Workers cluster.
test("named configurations save independently, preserve frozen Secrets, and survive reload", async ({
  page,
  context,
  baseURL,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));
  await page.goto("/?page=login");
  await page
    .getByRole("button", { name: /本地开发登录|kind 开发登录/ })
    .first()
    .click();
  await expect(page.getByRole("button", { name: "退出登录" })).toBeVisible();
  async function api(path, method = "GET", data) {
    const headers = { Origin: new URL(baseURL).origin };
    if (method !== "GET") {
      const csrf = await context.request.post("/api/auth/csrf", { headers });
      expect(csrf.ok()).toBe(true);
      headers["X-CSRF-Token"] = (await csrf.json()).csrf_token;
    }
    const response = await context.request.fetch(path, { method, headers, data });
    expect(response.ok(), method + " " + path + ": " + (await response.text())).toBe(true);
    return response.status() === 204 ? undefined : response.json();
  }
  const run = randomUUID().slice(0, 8);
  const org = await api("/api/v1/orgs", "POST", { name: "Configurations " + run });
  const orgBase = "/api/v1/orgs/" + org.id;
  const worker = await api(orgBase + "/workers", "POST", { name: "Configurations " + run });
  const base = orgBase + "/workers/" + worker.id;
  const path = base + "/environments/production/configurations";
  await page.goto(
    "/?page=workers&workspace=" + org.id + "&worker=" + worker.id + "&worker_tab=configurations",
  );
  await expect(page.getByRole("region", { name: "环境管理", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "预览管理", exact: true })).toHaveCount(0);
  await expect(page.getByText("生产环境", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "配置版本", exact: true }).click();
  await expect(
    page.getByText("尚无独立配置版本。可以从环境草稿或当前生效配置保存第一份。", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "从环境草稿创建", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "配置名称", exact: true }).fill("XXX 测试");
  await dialog.getByRole("button", { name: "添加变量", exact: true }).click();
  await dialog.getByRole("textbox", { name: "变量名 1", exact: true }).fill("API_URL");
  await dialog
    .getByRole("textbox", { name: "变量值 1", exact: true })
    .fill("https://test.example.com");
  await dialog.getByRole("button", { name: "添加 Secret", exact: true }).click();
  await dialog.getByRole("textbox", { name: "Secret 名称", exact: true }).fill("API_TOKEN");
  await dialog.getByLabel("Secret 值", { exact: true }).fill("configuration-browser-only-secret");
  await dialog.getByRole("button", { name: "加入此配置", exact: true }).click();
  await expect(dialog.getByText("待保存", { exact: true })).toBeVisible();
  expect((await api(path)).items).toHaveLength(0);
  await dialog.getByRole("button", { name: "保存新版本", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const detail = page.getByRole("region", { name: "配置版本详情", exact: true });
  await expect(detail.getByRole("heading", { name: "XXX 测试", exact: true })).toBeVisible();
  await expect(page.getByText("尚未部署", { exact: true }).first()).toBeVisible();
  const saved = (await api(path)).items[0];
  expect(saved.secret_names).toEqual(["API_TOKEN"]);
  expect(JSON.stringify(saved)).not.toContain("configuration-browser-only-secret");
  expect((await api(base + "/operations")).items).toHaveLength(0);
  expect((await api(base + "/environments")).items[0].variables).toEqual({});
  await detail.getByRole("button", { name: "复制并修改", exact: true }).click();
  await dialog.getByRole("textbox", { name: "配置名称", exact: true }).fill("支付重试测试");
  await dialog
    .getByRole("textbox", { name: "变量值 1", exact: true })
    .fill("https://retry.example.com");
  await dialog.getByRole("button", { name: "保存新版本", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(detail.getByRole("heading", { name: "支付重试测试", exact: true })).toBeVisible();
  expect((await api(path)).items).toHaveLength(2);
  await api(base + "/environments", "POST", { name: "test-" + run });
  await page.reload();
  const environments = page.getByRole("region", { name: "环境管理", exact: true });
  await expect(environments.getByText("生产环境", { exact: true })).toBeVisible();
  await expect(environments.getByText("预览环境", { exact: true })).toBeVisible();
  await expect(detail.getByRole("heading", { name: "支付重试测试", exact: true })).toBeVisible();
  const history = page.getByLabel("配置历史", { exact: true });
  await history.getByRole("button", { name: /XXX 测试/ }).click();
  await expect(detail).toContainText("https://test.example.com");
  await expect(detail).not.toContainText("https://retry.example.com");
  await expect(detail.getByRole("button", { name: "发布并生效", exact: true })).toBeDisabled();
  await page.screenshot({ path: ".data/configuration-versions-desktop.png", fullPage: true });
  await environments.getByRole("button", { name: new RegExp("test-" + run) }).click();
  await expect(
    page.getByText("尚无独立配置版本。可以从环境草稿或当前生效配置保存第一份。", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "从环境草稿创建", exact: true }).click();
  await dialog.getByRole("textbox", { name: "配置名称", exact: true }).fill("XXX 测试");
  await dialog.getByRole("button", { name: "添加变量", exact: true }).click();
  await dialog.getByRole("textbox", { name: "变量名 1", exact: true }).fill("API_URL");
  await dialog
    .getByRole("textbox", { name: "变量值 1", exact: true })
    .fill("https://preview.example.com");
  await dialog.getByRole("button", { name: "保存新版本", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(detail).toContainText("https://preview.example.com");
  await expect(detail).not.toContainText("https://test.example.com");
  expect((await api(base + "/environments/test-" + run + "/configurations")).items).toHaveLength(1);
  expect((await api(path)).items).toHaveLength(2);
  await page.screenshot({ path: ".data/configuration-preview-desktop.png", fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(detail.getByRole("heading", { name: "XXX 测试", exact: true })).toBeVisible();
  const fits = await page.evaluate(
    () => document.documentElement.scrollWidth <= window.innerWidth + 1,
  );
  expect(fits).toBe(true);
  await page.screenshot({ path: ".data/configuration-versions-mobile.png", fullPage: true });
  expect(errors).toEqual([]);
});
