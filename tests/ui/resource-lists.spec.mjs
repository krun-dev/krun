import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

// Exercise the static console and real Go/PostgreSQL APIs through one origin.
test("Workers and R2 lists search across pages and keep details addressable", async ({
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
  await page.goto("/");
  await page
    .getByRole("button", { name: /本地开发登录|kind 开发登录/ })
    .first()
    .click();
  await expect(page.getByRole("button", { name: "退出登录" })).toBeVisible();
  const run = randomUUID().slice(0, 8);
  const org = await api("/api/v1/orgs", "POST", { name: "List UI " + run });
  const base = "/api/v1/orgs/" + org.id;
  const profiles = await api(base + "/workers/network-profiles");
  const profile = profiles.items.find((item) => item.default)?.id || profiles.items[0].id;
  const resources = { workers: [], buckets: [] };
  for (let index = 0; index < 23; index++) {
    const suffix = String(index).padStart(2, "0");
    const service = await api(base + "/workers", "POST", { name: "List Worker " + suffix });
    await api(base + "/workers/" + service.id + "/network-profile", "PUT", {
      network_profile: profile,
      expected_revision: 0,
    });
    resources.workers.push(service);
    resources.buckets.push(
      await api(base + "/buckets", "POST", { name: "list-images-" + suffix }),
    );
  }
  for (const kind of ["workers", "buckets"]) {
    const label = kind === "workers" ? "搜索 Worker 名称或 ID" : "搜索桶名称或 ID";
    const back = kind === "workers" ? "返回Worker列表" : "返回桶列表";
    await page.goto("/?page=" + kind + "&workspace=" + org.id);
    await expect(page.locator("tbody tr")).toHaveCount(20);
    await expect(page.getByRole("columnheader", { name: "创建时间", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "上一页", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "下一页", exact: true }).click();
    await expect(page.locator("tbody tr")).toHaveCount(3);
    await expect(page.getByRole("button", { name: "下一页", exact: true })).toBeDisabled();

    const name = await page.locator("tbody tr").first().getByRole("button").first().textContent();
    const resource = resources[kind].find((item) => item.name === name);
    expect(resource).toBeTruthy();
    await page.locator("tbody tr").first().getByRole("button").first().click();
    await expect(page.locator("main time")).toBeVisible();
    expect(new Date(await page.locator("main time").getAttribute("datetime")).getTime()).toBe(
      new Date(resource.created_at).getTime(),
    );
    await page.getByRole("button", { name: back, exact: true }).click();
    await expect(page.locator("tbody tr")).toHaveCount(3);
    await expect(page.getByRole("navigation", { name: "列表分页" })).toContainText("第 2 页");

    // A search issued on page two must reset the cursor and include all pages.
    const target = resources[kind][0];
    await page.getByRole("textbox", { name: label, exact: true }).fill(target.id.toUpperCase());
    await page.getByRole("button", { name: "搜索", exact: true }).click();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.locator("tbody tr")).toContainText(target.name);
    await expect(page.getByRole("navigation", { name: "列表分页" })).toContainText("第 1 页");
    await page.locator("tbody tr").first().getByRole("button").first().click();
    await page.reload();
    await expect(page.locator("main time")).toBeVisible();
    expect(new Date(await page.locator("main time").getAttribute("datetime")).getTime()).toBe(
      new Date(target.created_at).getTime(),
    );
    await page.getByRole("button", { name: back, exact: true }).click();
    await expect(page.locator("tbody tr")).toHaveCount(20);

    await page.getByRole("textbox", { name: label, exact: true }).fill("no-such-resource");
    await page.getByRole("button", { name: "搜索", exact: true }).click();
    await expect(page.getByRole("heading", { name: "没有匹配结果", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "清除", exact: true }).click();
    await expect(page.locator("tbody tr")).toHaveCount(20);
    await page.getByRole("combobox", { name: "每页条数", exact: true }).selectOption("50");
    await expect(page.locator("tbody tr")).toHaveCount(23);
    await expect(page.getByRole("button", { name: "下一页", exact: true })).toBeDisabled();

    await page.setViewportSize({ width: 390, height: 844 });
    await expect(page.getByRole("textbox", { name: label, exact: true })).toBeVisible();
    await expect(page.locator("tbody tr").first().getByText(/创建时间/)).toBeVisible();
    await expect(page.getByRole("navigation", { name: "列表分页" })).toBeVisible();
    await page.screenshot({
      path: ".data/ui-resource-lists/" + kind + "-mobile.png",
      fullPage: true,
    });
    await page.setViewportSize({ width: 1440, height: 1000 });
  }
  expect(errors).toEqual([]);
});
