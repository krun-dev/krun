import { test, expect } from "@playwright/test";

test("quick start is public and uses the current origin", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const apiRequests = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname.startsWith("/api/")) apiRequests.push(request.url());
  });
  const response = await page.goto("/docs");
  expect(response.headers()["content-type"]).toContain("text/html");
  await expect(page.getByRole("heading", { name: "Quick Start." })).toBeVisible();
  await expect(page.getByRole("heading", { name: "2. 安装 CLI" })).toBeVisible();
  await expect(page.getByRole("link", { name: "查看原文" })).toHaveAttribute("href", "/quickstart.md");
  await expect(page.getByRole("link", { name: "LLM 文档索引" })).toHaveAttribute("href", "/llms.txt");
  const download = page.getByRole("link", { name: "macOS Apple Silicon" });
  await expect(download).toHaveAttribute("href", `${new URL(page.url()).origin}/downloads/krun-darwin-arm64`);
  await page.getByRole("button", { name: "复制 Markdown" }).click();
  await expect(page.getByRole("button", { name: "已复制" })).toBeVisible();
  const markdown = await page.evaluate(() => navigator.clipboard.readText());
  expect(markdown).toContain(`${new URL(page.url()).origin}/downloads/krun-darwin-arm64`);
  expect(markdown).toContain(`krun login --server ${new URL(page.url()).origin}`);
  expect((await page.request.get("/llms.txt")).status()).toBe(200);
  const raw = await page.request.get("/quickstart.md");
  expect(raw.status()).toBe(200);
  expect(await raw.text()).toContain("{{KRUN_ORIGIN}}");
  expect(markdown).not.toContain("{{KRUN_ORIGIN}}");
  await expect(page.locator("article")).toContainText(`krun login --server ${new URL(page.url()).origin}`);
  expect(apiRequests).toEqual([]);
});
