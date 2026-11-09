import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

// Real same-origin static console + Go + PostgreSQL. No browser API mocks.
// Requires object_storage in the main config; runtime publication/storage bytes
// are covered independently by the configured workerd/TOS integration tests.
test("UI manages buckets and persists environment and Preview Base R2 bindings", async ({
  page,
  context,
  baseURL,
}) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));
  async function api(path, method = "GET", data) {
    const headers = { origin: new URL(baseURL).origin };
    if (method !== "GET") {
      const token = await context.request.post("/api/auth/csrf", { headers });
      expect(token.ok()).toBeTruthy();
      headers["X-CSRF-Token"] = (await token.json()).csrf_token;
    }
    const response = await context.request.fetch(path, { method, headers, data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  }
  await page.goto("/?page=login");
  await page
    .getByRole("button", { name: /本地开发登录|kind 开发登录/ })
    .first()
    .click();
  await expect(page.getByRole("button", { name: "退出登录" })).toBeVisible();
  const run = randomUUID().slice(0, 8);
  const org = await api("/api/v1/orgs", "POST", { name: "R2 UI " + run });
  const organization = "/api/v1/orgs/" + org.id;
  const worker = await api(organization + "/workers", "POST", { name: "R2 worker " + run });
  const workers = organization + "/workers/" + worker.id;
  const profiles = await api(organization + "/workers/network-profiles");
  await api(workers + "/network-profile", "PUT", {
    network_profile: profiles.items.find((item) => item.default)?.id || profiles.items[0].id,
    expected_revision: 0,
  });
  const bucketName = "tu-pian-" + run;
  const bucketsURL = "/?page=buckets&workspace=" + org.id;
  const workerURL = "/?page=workers&workspace=" + org.id + "&worker=" + worker.id;
  await page.goto(bucketsURL);
  await expect(page.getByRole("heading", { name: "对象存储", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "创建桶", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "桶名称", exact: true }).fill(" ");
  await expect(dialog.getByRole("button", { name: "创建桶", exact: true })).toBeDisabled();
  await dialog.getByRole("textbox", { name: "桶名称", exact: true }).fill("图片 " + run);
  await dialog.getByRole("button", { name: "创建桶", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const row = page.getByRole("row").filter({ hasText: bucketName });
  await expect(row.getByText("已启用", { exact: true })).toBeVisible();
  await page.reload();
  await expect(row).toBeVisible();
  const cloudflare = await api("/api/v1/cloudflare/client/v4/accounts/" + org.id + "/r2/buckets");
  expect(cloudflare.success).toBe(true);
  expect(cloudflare.result.buckets.some((bucket) => bucket.name === bucketName)).toBe(true);

  await page.getByRole("button", { name: "创建桶", exact: true }).click();
  await dialog.getByRole("textbox", { name: "桶名称", exact: true }).fill(bucketName.toUpperCase());
  await dialog.getByRole("button", { name: "创建桶", exact: true }).click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  await page.keyboard.press("Escape");

  async function settings() {
    await page.goto(workerURL);
    await page.getByRole("tab", { name: "配置版本", exact: true }).click();
    const draft = page
      .locator("details")
      .filter({ has: page.getByText("管理环境草稿", { exact: true }) });
    if ((await draft.getAttribute("open")) === null)
      await draft.locator(":scope > summary").click();
  }
  async function environment() {
    return (await api(workers + "/environments")).items.find((item) => item.name === "production");
  }
  await settings();
  await page.getByRole("button", { name: "添加对象存储绑定", exact: true }).click();
  await page.getByRole("textbox", { name: "对象存储绑定名称 1", exact: true }).fill("BUCKET");
  await page.getByRole("combobox", { name: "存储桶 1", exact: true }).selectOption(bucketName);
  await page.getByRole("button", { name: "保存对象存储绑定", exact: true }).click();
  await expect
    .poll(async () => (await environment()).r2_buckets?.BUCKET?.bucket_name)
    .toBe(bucketName);
  await settings();
  await expect(page.getByRole("textbox", { name: "对象存储绑定名称 1", exact: true })).toHaveValue(
    "BUCKET",
  );
  await expect(page.getByRole("combobox", { name: "存储桶 1", exact: true })).toHaveValue(
    bucketName,
  );
  await page
    .getByRole("region", { name: "对象存储", exact: true })
    .screenshot({ path: ".data/ui-r2/binding.png" });
  await page.getByRole("button", { name: "添加对象存储绑定", exact: true }).click();
  await page.getByRole("textbox", { name: "对象存储绑定名称 2", exact: true }).fill("BUCKET");
  await page.getByRole("combobox", { name: "存储桶 2", exact: true }).selectOption(bucketName);
  await page.getByRole("button", { name: "保存对象存储绑定", exact: true }).click();
  await expect(
    page.getByText("对象存储绑定名称重复，或与变量、Secret、版本元数据冲突。", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "移除对象存储绑定 2", exact: true }).click();
  await page.getByRole("button", { name: "添加变量", exact: true }).click();
  await page.getByRole("textbox", { name: "变量名 1", exact: true }).fill("BUCKET");
  await page.getByRole("textbox", { name: "变量值 1", exact: true }).fill("collision");
  await page.getByRole("button", { name: "保存环境变量", exact: true }).click();
  await expect(
    page.getByText(/binding.*conflict|绑定.*冲突|binding.*already/i).first(),
  ).toBeVisible();
  await page.getByRole("button", { name: "加载最新配置", exact: true }).click();

  await page.getByRole("button", { name: "创建预览环境", exact: true }).click();
  await page.getByText("新建预览默认配置", { exact: true }).first().click();
  await page.getByRole("button", { name: "添加对象存储绑定", exact: true }).click();
  await page
    .getByRole("textbox", { name: "对象存储绑定名称 1", exact: true })
    .fill("PREVIEW_BUCKET");
  await page.getByRole("combobox", { name: "存储桶 1", exact: true }).selectOption(bucketName);
  await page.getByRole("button", { name: "保存对象存储绑定", exact: true }).click();
  await expect
    .poll(
      async () => (await api(workers + "/previews-base")).r2_buckets?.PREVIEW_BUCKET?.bucket_name,
    )
    .toBe(bucketName);
  await dialog.getByRole("textbox", { name: "预览名称", exact: true }).fill("preview-" + run);
  await dialog.getByRole("button", { name: "创建", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect
    .poll(
      async () =>
        (await api(workers + "/environments")).items.find((item) => item.name === "preview-" + run)
          ?.r2_buckets?.PREVIEW_BUCKET?.bucket_name,
    )
    .toBe(bucketName);

  await page.goto(bucketsURL);
  await row.getByRole("button", { name: "停用", exact: true }).click();
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await expect(row.getByText("已停用", { exact: true })).toBeVisible();
  await settings();
  await expect(
    page.getByRole("combobox", { name: "存储桶 1", exact: true }).locator("option:checked"),
  ).toHaveText(bucketName + " · 已停用");
  await page.getByRole("button", { name: "保存对象存储绑定", exact: true }).click();
  await expect(
    page
      .getByText(/R2 bucket does not exist|存储桶|存储桶/)
      .filter({ hasText: /停用|disabled|不可用|unavailable|不存在/ })
      .first(),
  ).toBeVisible();
  expect((await environment()).r2_buckets.BUCKET.bucket_name).toBe(bucketName);
  await page.goto(bucketsURL);
  await row.getByRole("button", { name: "启用", exact: true }).click();
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  await expect(row.getByText("已启用", { exact: true })).toBeVisible();
  await row.getByRole("button", { name: "停用", exact: true }).click();
  const bucket = (await api(organization + "/buckets")).items.find(
    (item) => item.name === bucketName,
  );
  await api(organization + "/buckets/" + bucket.id, "PATCH", {
    enabled: true,
    revision: bucket.revision,
  });
  const conflict = page.waitForResponse(
    (response) =>
      response.request().method() === "PATCH" && response.url().endsWith("/buckets/" + bucket.name),
  );
  await dialog.getByRole("button", { name: "确认", exact: true }).click();
  expect((await conflict).status()).toBe(409);
  await expect(dialog).not.toBeVisible();
  await expect(row.getByText("已启用", { exact: true })).toBeVisible();
  await settings();
  await page.getByRole("button", { name: "移除对象存储绑定 1", exact: true }).click();
  await page.getByRole("button", { name: "保存对象存储绑定", exact: true }).click();
  await expect.poll(async () => Object.keys((await environment()).r2_buckets || {}).length).toBe(0);
  await settings();
  await expect(page.getByText("尚未配置对象存储绑定。", { exact: true })).toBeVisible();

  const other = await api("/api/v1/orgs", "POST", { name: "Other R2 UI " + run });
  expect((await api("/api/v1/orgs/" + other.id + "/buckets")).items).toEqual([]);
  await page.goto(bucketsURL);
  await expect(row).toBeVisible();
  await page.screenshot({ path: ".data/ui-r2/desktop.png", fullPage: true });
  await page.getByRole("combobox", { name: "语言", exact: true }).selectOption("en");
  await expect(page.getByRole("heading", { name: "Object storage", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Create bucket", exact: true })).toBeVisible();
  await page.getByRole("combobox", { name: "Language", exact: true }).selectOption("zh-CN");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(bucketsURL);
  await expect(row).toBeVisible();
  await expect(row.getByRole("button", { name: "停用", exact: true })).toBeInViewport();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  await page.screenshot({ path: ".data/ui-r2/mobile.png", fullPage: true });
  expect(errors).toEqual([]);
});
