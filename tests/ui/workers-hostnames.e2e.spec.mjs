import { mkdir } from "node:fs/promises";
import { expect, test } from "@playwright/test";

test("claim and edit a shared hostname through the console", async ({ page, context, baseURL }) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));

  async function request(path, method = "GET", data) {
    const headers = { origin: new URL(baseURL).origin };
    if (method !== "GET") {
      const csrf = await context.request.post("/api/auth/csrf", { headers });
      expect(csrf.ok()).toBeTruthy();
      headers["X-CSRF-Token"] = (await csrf.json()).csrf_token;
    }
    const result = await context.request.fetch(path, { method, headers, data });
    expect(result.ok(), await result.text()).toBeTruthy();
    return result.json();
  }

  await page.goto("/?page=login");
  await page
    .getByRole("button", { name: /本地开发登录|kind 开发登录/ })
    .first()
    .click();
  await expect(page.getByRole("button", { name: "退出登录" })).toBeVisible();

  const configuredOrg = process.env.KRUN_HOSTNAME_TEST_ORG;
  const org = configuredOrg
    ? await request("/api/v1/orgs/" + configuredOrg)
    : await request("/api/v1/orgs", "POST", {
        name: process.env.KRUN_HOSTNAME_SCREENSHOTS ? "示例工作空间" : "Domain Demo " + Date.now(),
        short_name: "domain-demo-" + Date.now(),
      });
  const orgBase = "/api/v1/orgs/" + org.id;
  const catalog = await request(orgBase + "/domains");
  test.skip(!catalog.suffixes.length, "The test deployment must enable a managed suffix");
  const profiles = await request(orgBase + "/workers/network-profiles");
  const profile = profiles.items.find((item) => item.default);
  expect(profile).toBeTruthy();
  for (const [name, locator] of [
    ["Storefront", "storefront"],
    ["Orders API", "orders"],
  ]) {
    if (!configuredOrg) {
      const worker = await request(orgBase + "/workers", "POST", { name, locator });
      await request(orgBase + "/workers/" + worker.id + "/network-profile", "PUT", {
        network_profile: profile.id,
        expected_revision: 0,
      });
    }
  }

  const suffix = catalog.suffixes[0];
  const staticOrigin = catalog.origins.find(
    (origin) => origin.kind === "static" && origin.suffixes.includes(suffix.id),
  );
  const httpOrigin = catalog.origins.find(
    (origin) => origin.kind === "http" && origin.suffixes.includes(suffix.id),
  );
  const desiredLabel =
    process.env.KRUN_HOSTNAME_TEST_LABEL ||
    (process.env.KRUN_HOSTNAME_SCREENSHOTS ? "shop" : "shop-" + Date.now());
  const host = desiredLabel + "." + catalog.suffixes[0].domain;
  const screenshotDir = process.env.KRUN_HOSTNAME_SCREENSHOTS;
  if (screenshotDir) await mkdir(screenshotDir, { recursive: true });

  async function screenshot(file, anchors) {
    if (!screenshotDir) return;
    await page.evaluate(() => {
      const layer = document.createElement("div");
      layer.id = "documentation-annotation-layer";
      layer.style.cssText = "position:fixed;inset:0;z-index:10000;pointer-events:none";
      layer.setAttribute("aria-hidden", "true");
      document.body.append(layer);
    });
    for (const [number, locator] of anchors) {
      const box = await locator.boundingBox();
      expect(box).toBeTruthy();
      await page.evaluate(
        ({ number, box }) => {
          const badge = document.createElement("span");
          badge.textContent = String(number);
          badge.style.cssText =
            "position:absolute;display:grid;place-items:center;width:24px;height:24px;border-radius:50%;font:bold 14px system-ui;background:#b91c1c;color:white;border:2px solid white;box-shadow:0 2px 6px #0006";
          badge.style.left = Math.max(0, box.x - 9) + "px";
          badge.style.top = Math.max(0, box.y - 9) + "px";
          document.getElementById("documentation-annotation-layer").append(badge);
        },
        { number, box },
      );
    }
    await page.screenshot({ path: screenshotDir + "/" + file, animations: "disabled" });
    await page.locator("#documentation-annotation-layer").evaluate((element) => element.remove());
  }

  await page.goto("/?page=workers&workspace=" + org.id);
  const domains = page.getByRole("button", { name: "应用域名", exact: true });
  await domains.click();
  const claim = page.getByRole("button", { name: "申请域名" });
  await claim.click();
  const dialog = page.getByRole("dialog");
  const prefix = dialog.getByRole("textbox", { name: "域名前缀" });
  const submitClaim = dialog.getByRole("button", { name: "申请域名", exact: true });
  const save = dialog.getByRole("button", { name: "保存", exact: true });
  await prefix.fill(desiredLabel);
  await screenshot("claim-domain-prefix-first.png", [
    [3, prefix],
    [4, dialog.getByRole("combobox", { name: "域名后缀" })],
    [5, submitClaim],
  ]);
  const claimed = page.waitForResponse(
    (response) => response.url().endsWith("/domains") && response.request().method() === "POST",
  );
  await submitClaim.click();
  const response = await claimed;
  expect(response.status(), await response.text()).toBe(201);

  await expect(page.getByRole("link", { name: host })).toBeVisible();
  const edit = page.getByRole("button", { name: "编辑路由" });
  await edit.click();
  await dialog.getByRole("button", { name: "添加路由" }).click();
  const targets = dialog.getByRole("combobox", { name: "目标类型" });
  if (staticOrigin) {
    await targets.nth(0).selectOption(staticOrigin.host);
  } else {
    await dialog.getByRole("textbox", { name: "目标 Worker" }).first().fill("storefront");
  }
  await dialog.getByRole("button", { name: "添加路由" }).click();
  await dialog.getByRole("textbox", { name: "目标 Worker" }).last().fill("orders");
  if (httpOrigin) {
    await dialog.getByRole("button", { name: "添加路由" }).click();
    await dialog.getByRole("textbox", { name: "请求路径" }).last().fill("/legacy");
    await targets.last().selectOption(httpOrigin.host);
  }
  await save.scrollIntoViewIfNeeded();
  await screenshot("edit-domain-routes.png", [
    [6, dialog.getByRole("textbox", { name: "请求路径" }).nth(1)],
    [7, targets.nth(0)],
    [8, save],
  ]);
  await save.click();
  await expect(dialog).toBeHidden();
  await expect(page.getByText("/api", { exact: true })).toBeVisible();
  if (staticOrigin) await expect(page.getByText(staticOrigin.host, { exact: true })).toBeVisible();
  if (httpOrigin) await expect(page.getByText(httpOrigin.host, { exact: true })).toBeVisible();
  await screenshot("network-domain-list.png", [
    [1, domains],
    [2, edit],
  ]);
  await page.reload();
  await expect(page.getByRole("link", { name: host })).toBeVisible();
  await edit.click();
  await dialog.getByRole("checkbox", { name: "启用路由" }).nth(1).uncheck();
  await save.click();
  await expect(page.getByText("已停用", { exact: true })).toBeVisible();

  await page.goto("/?page=workers&workspace=" + org.id + "&worker=orders&worker_tab=routing");
  await expect(page.getByRole("link", { name: host })).toBeVisible();
  const bindings = await request(orgBase + "/workers/orders/hostnames");
  if (bindings.suffixes.length) {
    const suffix = bindings.suffixes[0];
    const customLabel = "orders-" + Date.now();
    const customHost = customLabel + "." + suffix.domain;
    await request(orgBase + "/workers/orders/environments", "POST", { name: "pr-123" });
    await page.getByRole("button", { name: "绑定自定义域名", exact: true }).click();
    await dialog.getByRole("textbox", { name: "域名前缀" }).fill(customLabel);
    await expect(dialog.getByRole("combobox", { name: "域名后缀" })).toHaveValue(suffix.id);
    await dialog.getByRole("button", { name: "绑定域名", exact: true }).click();
    await expect(page.getByRole("link", { name: customHost, exact: true })).toBeVisible();
    const preview = page.getByRole("link", { name: "pr-123." + customHost, exact: true });
    await expect(preview).toBeVisible();
    await expect(page.getByText("生产和 Preview", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "解绑自定义域名", exact: true }).click();
    await dialog.getByRole("button", { name: "确认解绑" }).click();
    await expect(preview).toHaveCount(0);
  }
  await page.getByRole("link", { name: "管理应用域名" }).click();
  await expect(page).toHaveURL(/page=domains/);

  // Exercise both palettes while the real route table is rendered.
  await page.evaluate(() => document.documentElement.classList.add("dark"));
  await expect(page.getByRole("link", { name: host })).toBeVisible();

  await edit.click();
  await dialog.getByRole("button", { name: "退役域名" }).click();
  await dialog.getByRole("button", { name: "确认退役" }).click();
  await expect(page.getByRole("link", { name: host })).toHaveCount(0);
  expect(errors).toEqual([]);
});
