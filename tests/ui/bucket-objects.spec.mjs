import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

// Static console + Go + PostgreSQL + a real S3 provider with browser CORS.
// No route interception or API mocks. Use disposable logical buckets only.
test("browse directories and transfer object bytes directly to storage", async ({
  page,
  context,
  baseURL,
}) => {
  await page.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));
  const failures = [];
  const transfers = [];
  page.on("pageerror", (error) => failures.push(error.message));
  page.on("request", (request) => {
    if (request.method() === "PUT" && new URL(request.url()).origin !== new URL(baseURL).origin) {
      transfers.push(request);
    }
    if (
      new URL(request.url()).origin === new URL(baseURL).origin &&
      request.url().includes("/objects")
    ) {
      expect(request.postDataBuffer()?.byteLength || 0).toBeLessThan(4096);
    }
  });
  async function api(path, method = "GET", data) {
    const headers = { origin: new URL(baseURL).origin };
    if (method !== "GET") {
      const csrf = await context.request.post("/api/auth/csrf", { headers });
      expect(csrf.ok()).toBeTruthy();
      headers["X-CSRF-Token"] = (await csrf.json()).csrf_token;
    }
    const response = await context.request.fetch(path, { method, headers, data });
    expect(response.ok(), "control-plane request failed").toBeTruthy();
    return response.json();
  }
  await page.goto("/");
  await page.getByRole("button", { name: /本地开发登录|kind 开发登录/ }).first().click();
  await expect(page.getByRole("button", { name: "退出登录" })).toBeVisible();
  const id = randomUUID().slice(0, 8);
  const org = await api("/api/v1/orgs", "POST", { name: "Object browser " + id });
  const base = "/api/v1/orgs/" + org.id;
  const bucket = await api(base + "/buckets", "POST", { name: "browser-" + id });
  const objects = base + "/buckets/" + bucket.id + "/objects";
  const keys = ["目录/hello +?#%.txt", "目录/empty.txt", "目录/sub/nested.txt"];
  try {
    await page.goto("/?page=buckets&workspace=" + org.id);
    await page.getByRole("button", { name: bucket.name, exact: true }).click();
    await expect(page.getByRole("heading", { name: bucket.name, exact: true })).toBeVisible();
    await expect(page.getByText("目录为空", { exact: true })).toBeVisible();
    await page.getByRole("textbox", { name: "路径前缀", exact: true }).fill("目录/");
    await page.getByRole("button", { name: "打开目录", exact: true }).click();
    const content = Buffer.from("Browser direct upload. 中文。\n".repeat(4096));
    await page.getByLabel("上传文件", { exact: true }).setInputFiles([
      { name: "hello +?#%.txt", mimeType: "text/plain", buffer: content },
      { name: "empty.txt", mimeType: "text/plain", buffer: Buffer.alloc(0) },
    ]);
    await expect(page.getByRole("button", { name: "hello +?#%.txt", exact: true })).toBeVisible();
    expect(transfers.length).toBe(2);
    expect(
      transfers.every((request) => !request.headers()["x-csrf-token"] && !request.headers().cookie),
    ).toBe(true);
    const detail = await api(objects + "/head?" + new URLSearchParams({ key: keys[0] }));
    expect(detail.size).toBe(content.length);
    expect(detail.r2_compatible).toBe(true);
    expect(detail.http_metadata.contentType).toBe("text/plain");
    await page.getByRole("button", { name: "hello +?#%.txt", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("兼容", { exact: true })).toBeVisible();
    await expect(dialog.getByText("text/plain", { exact: false })).toBeVisible();
    await page.keyboard.press("Escape");
    const row = page.getByRole("row").filter({ hasText: "hello +?#%.txt" });
    const downloadPromise = page.waitForEvent("download");
    await row.getByRole("link", { name: "下载", exact: true }).click();
    const download = await downloadPromise;
    // Chromium replaces reserved filename characters; the object key stays intact.
    expect(download.suggestedFilename()).toBe("hello +_#%.txt");
    const stream = await download.createReadStream();
    const chunks = [];
    for await (const chunk of stream) {
      chunks.push(chunk);
    }
    expect(Buffer.concat(chunks).equals(content)).toBe(true);
    const redirected = await context.request.get(
      objects + "/download?" + new URLSearchParams({ key: keys[0] }),
      { maxRedirects: 0 },
    );
    expect(redirected.status()).toBe(302);
    expect(new URL(redirected.headers().location).origin).not.toBe(new URL(baseURL).origin);
    expect((await redirected.body()).length).toBeLessThan(4096);

    await page.getByRole("textbox", { name: "路径前缀", exact: true }).fill("目录/sub/");
    await page.getByRole("button", { name: "打开目录", exact: true }).click();
    await page
      .getByLabel("上传文件", { exact: true })
      .setInputFiles({ name: "nested.txt", mimeType: "text/plain", buffer: Buffer.from("nested") });
    await expect(page.getByRole("button", { name: "nested.txt", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "根目录", exact: true }).click();
    await page.getByRole("button", { name: "目录/", exact: true }).click();
    await expect(page.getByRole("button", { name: "sub/", exact: true })).toBeVisible();
    await page.reload();
    await expect(page.getByRole("textbox", { name: "路径前缀", exact: true })).toHaveValue("目录/");
    await expect(page.getByRole("button", { name: "hello +?#%.txt", exact: true })).toBeVisible();
    const cf = await api(
      "/api/v1/cloudflare/client/v4/accounts/" +
        org.id +
        "/r2/buckets/" +
        bucket.name +
        "/objects?delimiter=/&prefix=" +
        encodeURIComponent("目录/"),
    );
    expect(cf.success).toBe(true);
    expect(cf.result.length).toBe(2);
    expect(cf.result_info.delimited).toEqual(["目录/sub/"]);
    await page
      .getByRole("row")
      .filter({ hasText: "empty.txt" })
      .getByRole("button", { name: "删除", exact: true })
      .click();
    await page.getByRole("dialog").getByRole("button", { name: "确认删除", exact: true }).click();
    await expect(page.getByRole("row").filter({ hasText: "empty.txt" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "hello +?#%.txt", exact: true })).toBeVisible();
    await page.screenshot({ path: ".data/ui-r2/object-browser-desktop.png", fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBe(true);
    await page.screenshot({ path: ".data/ui-r2/object-browser-mobile.png", fullPage: true });
    expect(failures).toEqual([]);
  } finally {
    for (const key of keys) {
      await api(objects + "?" + new URLSearchParams({ key }), "DELETE");
    }
  }
});
