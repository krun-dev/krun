import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

test.use({ actionTimeout: 20_000 });

// Uses the isolated object-browser fixture: real Go, PostgreSQL and MinIO,
// with the built static console served at the same origin as /api/.
test("preview defaults save independently and Secrets can be replaced and deleted", async ({
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
  const org = await api("/api/v1/orgs", "POST", { name: "Preview settings " + run });
  const orgBase = "/api/v1/orgs/" + org.id;
  const worker = await api(orgBase + "/workers", "POST", { name: "Preview settings " + run });
  const base = orgBase + "/workers/" + worker.id;
  const profiles = await api(orgBase + "/workers/network-profiles");
  await api(base + "/network-profile", "PUT", {
    network_profile: profiles.items.find((item) => item.default)?.id || profiles.items[0].id,
    expected_revision: 0,
  });
  const bucket = await api(orgBase + "/buckets", "POST", { name: "preview-settings-" + run });
  const original = { TEXT: "false", CONFIG: { nested: [1, true, null] }, COUNT: 7 };
  await api(base + "/previews-base", "PATCH", { variables: original, expected_revision: 0 });
  await page.goto("/?page=workers&workspace=" + org.id + "&worker=" + worker.id);
  await page.getByRole("tab", { name: "预览基础配置", exact: true }).click();
  const variables = page.getByRole("region", { name: "环境变量", exact: true });
  const secrets = page.getByRole("region", { name: "Secrets", exact: true });
  const variable = (name) =>
    variables.locator("div.rounded-md.border").filter({
      has: page.locator('input[value="' + name + '"]'),
    });
  await expect(variable("TEXT").getByRole("textbox").last()).toHaveValue("false");
  await expect(variable("TEXT").getByRole("combobox")).toHaveValue("text");
  await expect(
    page.getByRole("textbox", { name: "版本信息变量名", exact: true }),
  ).not.toBeVisible();

  // An invalid variable draft must not block or be submitted with an R2 save.
  await variable("CONFIG").getByRole("textbox").last().fill("{");
  await page.getByRole("button", { name: "添加对象存储绑定", exact: true }).click();
  await page.getByRole("textbox", { name: "对象存储绑定名称 1", exact: true }).fill("BUCKET");
  await page.getByRole("combobox", { name: "存储桶 1", exact: true }).selectOption(bucket.name);
  await page.getByRole("button", { name: "保存对象存储绑定", exact: true }).click();
  await expect
    .poll(async () => (await api(base + "/previews-base")).r2_buckets?.BUCKET?.bucket_name)
    .toBe(bucket.name);
  expect((await api(base + "/previews-base")).variables).toEqual(original);
  await expect(variable("CONFIG").getByRole("textbox").last()).toHaveValue("{");

  // Secrets have one row per name; the eye reveals only the current input.
  await secrets.getByRole("button", { name: "添加 Secret", exact: true }).click();
  await secrets.getByRole("textbox", { name: "Secret 名称", exact: true }).fill("API_TOKEN");
  await secrets.getByLabel("Secret 值", { exact: true }).fill("first-test-value");
  await expect(secrets.getByLabel("Secret 值", { exact: true })).toHaveAttribute(
    "type",
    "password",
  );
  await secrets.getByRole("button", { name: "显示输入值", exact: true }).click();
  await expect(secrets.getByLabel("Secret 值", { exact: true })).toHaveAttribute("type", "text");
  await secrets.getByRole("button", { name: "保存 Secret", exact: true }).click();
  await expect(secrets.getByRole("listitem")).toHaveCount(1);
  await expect(secrets.getByText("API_TOKEN", { exact: true })).toBeVisible();
  await expect(secrets.getByRole("button", { name: "显示输入值", exact: true })).toHaveCount(0);
  await expect(variable("CONFIG").getByRole("textbox").last()).toHaveValue("{");
  const inherited = await api(base + "/environments", "POST", { name: "before-" + run });
  expect(inherited.secret_names).toEqual(["API_TOKEN"]);

  await secrets.getByRole("button", { name: "替换 Secret API_TOKEN", exact: true }).click();
  await expect(secrets.getByLabel("新的 Secret 值", { exact: true })).toHaveValue("");
  await expect(secrets.getByLabel("新的 Secret 值", { exact: true })).toHaveAttribute(
    "type",
    "password",
  );
  await expect(secrets.getByRole("textbox", { name: "Secret 名称", exact: true })).toBeDisabled();
  await secrets.getByLabel("新的 Secret 值", { exact: true }).fill("replacement-test-value");
  await secrets.getByRole("button", { name: "保存 Secret", exact: true }).click();
  await expect(secrets.getByRole("button", { name: "添加 Secret", exact: true })).toBeVisible();
  await expect(secrets.getByRole("listitem")).toHaveCount(1);
  const publicData = JSON.stringify(await api(base + "/previews-base"));
  expect(publicData).not.toContain("first-test-value");
  expect(publicData).not.toContain("replacement-test-value");

  await secrets.getByRole("button", { name: "删除 Secret API_TOKEN", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("仅影响之后新建的 Preview");
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  expect((await api(base + "/previews-base")).secret_names).toEqual(["API_TOKEN"]);
  await secrets.getByRole("button", { name: "删除 Secret API_TOKEN", exact: true }).click();
  await dialog.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(secrets.getByRole("listitem")).toHaveCount(0);
  const environments = await api(base + "/environments");
  expect(environments.items.find((item) => item.name === inherited.name).secret_names).toEqual([
    "API_TOKEN",
  ]);
  const after = await api(base + "/environments", "POST", { name: "after-" + run });
  expect(after.secret_names || []).toEqual([]);

  // Saving variables neither parses nor clears an incomplete R2 draft.
  await variable("CONFIG").getByRole("textbox").last().fill('{"nested":[2,false,null]}');
  await page.getByRole("button", { name: "添加对象存储绑定", exact: true }).click();
  await page.getByText("高级设置：在代码中读取版本信息", { exact: true }).click();
  await page.getByRole("textbox", { name: "版本信息变量名", exact: true }).fill("VERSION_INFO");
  await variables.getByRole("button", { name: "保存环境变量", exact: true }).click();
  await expect
    .poll(async () => (await api(base + "/previews-base")).variables.CONFIG)
    .toEqual({ nested: [2, false, null] });
  await expect(page.getByRole("textbox", { name: "对象存储绑定名称 2", exact: true })).toHaveValue("");
  expect(Object.keys((await api(base + "/previews-base")).r2_buckets)).toEqual(["BUCKET"]);
  await page.getByRole("button", { name: "加载最新配置", exact: true }).click();
  await expect(page.getByRole("textbox", { name: "对象存储绑定名称 2", exact: true })).toHaveCount(0);

  // A stale save must report a conflict and retain the unsaved draft.
  const current = await api(base + "/previews-base");
  await api(base + "/previews-base", "PATCH", {
    variables: { EXTERNAL: "newer" },
    expected_revision: current.configuration_revision,
  });
  await variable("TEXT").getByRole("textbox").last().fill("unsaved");
  await variables.getByRole("button", { name: "保存环境变量", exact: true }).click();
  await expect(page.getByRole("status")).toContainText(/配置.*变|configuration changed/i);
  await expect(variable("TEXT").getByRole("textbox").last()).toHaveValue("unsaved");
  await page.getByRole("button", { name: "加载最新配置", exact: true }).click();
  await expect(variables.getByRole("textbox", { name: "变量名 1", exact: true })).toHaveValue(
    "EXTERNAL",
  );

  await page.getByRole("tab", { name: "环境配置", exact: true }).click();
  await secrets.getByRole("button", { name: "添加 Secret", exact: true }).click();
  await secrets.getByRole("textbox", { name: "Secret 名称", exact: true }).fill("ENV_TOKEN");
  await secrets.getByLabel("Secret 值", { exact: true }).fill("environment-test-value");
  await secrets.getByRole("button", { name: "保存 Secret", exact: true }).click();
  await expect(
    secrets.getByRole("button", { name: "替换 Secret ENV_TOKEN", exact: true }),
  ).toBeVisible();
  await secrets.getByRole("button", { name: "替换 Secret ENV_TOKEN", exact: true }).click();
  await expect(secrets.getByLabel("新的 Secret 值", { exact: true })).toHaveValue("");
  await secrets.getByLabel("新的 Secret 值", { exact: true }).fill("environment-replacement");
  await secrets.getByRole("button", { name: "保存 Secret", exact: true }).click();
  await expect(secrets.getByRole("button", { name: "添加 Secret", exact: true })).toBeVisible();
  await secrets.getByRole("button", { name: "删除 Secret ENV_TOKEN", exact: true }).click();
  await expect(dialog).toContainText("保存后在首次部署时生效。");
  await dialog.getByRole("button", { name: "确认删除", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(secrets.getByRole("listitem")).toHaveCount(0);
  await page.getByRole("tab", { name: "预览基础配置", exact: true }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  await page.screenshot({ path: ".data/preview-settings-mobile.png", fullPage: true });
  await page.getByRole("combobox", { name: "语言", exact: true }).selectOption("en");
  await expect(
    page.getByRole("heading", { name: "Defaults for new Preview environments", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Save object storage bindings", exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});
