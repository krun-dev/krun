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

  const org = await request("/api/v1/orgs", "POST", {
    name: process.env.KRUN_HOSTNAME_SCREENSHOTS ? "示例工作空间" : "Domain Demo " + Date.now(),
  });
  const orgBase = "/api/v1/orgs/" + org.id;
  const catalog = await request(orgBase + "/worker-hostnames");
  test.skip(!catalog.suffixes.length, "The test deployment must enable a managed suffix");
  const profiles = await request(orgBase + "/workers/network-profiles");
  const profile = profiles.items.find((item) => item.default);
  expect(profile).toBeTruthy();
  for (const [name, locator] of [
    ["Storefront", "storefront"],
    ["Orders API", "orders"],
  ]) {
    const worker = await request(orgBase + "/workers", "POST", { name, locator });
    await request(orgBase + "/workers/" + worker.id + "/network-profile", "PUT", {
      network_profile: profile.id,
      expected_revision: 0,
    });
  }

  const desiredLabel = process.env.KRUN_HOSTNAME_SCREENSHOTS ? "shop" : "shop-" + Date.now();
  const host = desiredLabel + "." + catalog.suffixes[0].domain;
  const screenshotDir = process.env.KRUN_HOSTNAME_SCREENSHOTS;
  if (screenshotDir) await mkdir(screenshotDir, { recursive: true });

  async function screenshot(file, anchors) {
    if (!screenshotDir) return;
    await page.evaluate(() => {
      const layer = document.createElement("div");
      layer.id = "documentation-annotation-layer";
      layer.style.cssText = "position:fixed;inset:0;z-index:10000;pointer-events:none";
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
  const catalogTab = page.getByRole("tab", { name: "域名与路由" });
  await catalogTab.click();
  await page.getByRole("button", { name: "申请域名" }).click();
  const dialog = page.getByRole("dialog");
  const prefix = dialog.getByRole("textbox", { name: "域名前缀" });
  await prefix.fill(desiredLabel);
  await dialog.getByRole("button", { name: "添加路由" }).click();
  await dialog.getByRole("textbox", { name: "目标 Worker" }).nth(0).fill("storefront");
  await dialog.getByRole("button", { name: "添加路由" }).click();
  await dialog.getByRole("textbox", { name: "目标 Worker" }).nth(1).fill("orders");
  const save = dialog.getByRole("button", { name: "保存", exact: true });
  await screenshot("claim.png", [
    [3, prefix],
    [4, dialog.getByRole("textbox", { name: "目标 Worker" }).nth(1)],
    [5, save],
  ]);
  const claimed = page.waitForResponse(
    (response) =>
      response.url().endsWith("/worker-hostnames") && response.request().method() === "POST",
  );
  await save.click();
  const response = await claimed;
  expect(response.status(), await response.text()).toBe(201);

  await expect(page.getByRole("link", { name: host })).toBeVisible();
  const edit = page.getByRole("button", { name: "编辑路由" });
  await screenshot("catalog.png", [
    [1, catalogTab],
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
  await expect(page.getByRole("button", { name: "管理域名与路由" })).toBeVisible();
  await page.getByRole("button", { name: "管理域名与路由" }).click();
  await expect(catalogTab).toHaveAttribute("aria-selected", "true");

  // Exercise both palettes while the real route table is rendered.
  await page.evaluate(() => document.documentElement.classList.add("dark"));
  await expect(page.getByRole("link", { name: host })).toBeVisible();

  await edit.click();
  await dialog.getByRole("button", { name: "退役域名" }).click();
  await dialog.getByRole("button", { name: "确认退役" }).click();
  await expect(page.getByRole("link", { name: host })).toHaveCount(0);
  expect(errors).toEqual([]);
});
