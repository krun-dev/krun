import { test, expect } from "@playwright/test";

test("quick start is public and uses the current origin", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const apiRequests = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname.startsWith("/api/")) apiRequests.push(request.url());
  });
  const response = await page.goto("/docs/");
  expect(response.headers()["content-type"]).toContain("text/html");
  await expect(page.getByRole("heading", { name: "Quick Start" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "2. 安装 CLI" })).toBeVisible();
  await expect(page.getByRole("link", { name: "查看原文" })).toHaveAttribute("href", "/docs/index.md.txt");
  await expect(page.getByRole("link", { name: "LLM 文档索引" })).toHaveAttribute(
    "href",
    "/docs/llms.txt",
  );
  const download = page.getByRole("link", { name: "macOS Apple Silicon" });
  await expect(download).toHaveAttribute("href", "/downloads/krun-darwin-arm64");
  await page.getByRole("button", { name: "复制 Markdown" }).click();
  await expect(page.getByRole("button", { name: "已复制" })).toBeVisible();
  const markdown = await page.evaluate(() => navigator.clipboard.readText());
  expect(markdown).toContain(`${new URL(page.url()).origin}/downloads/krun-darwin-arm64`);
  expect(markdown).toContain(`krun config set endpoint ${new URL(page.url()).origin}`);
  expect((await page.request.get("/docs/llms.txt")).status()).toBe(200);
  await expect(page.getByRole("link", { name: "基础概念", exact: true })).toHaveAttribute(
    "href",
    "/docs/guides/concepts.html",
  );
  const view = await page.request.get("/docs/index.md.txt");
  expect(view.headers()["content-type"]).toContain("text/plain");
  const raw = await page.request.get("/docs/index.md");
  expect(raw.status()).toBe(200);
  expect(await raw.text()).toContain("{{KRUN_ORIGIN}}");
  expect(markdown).not.toContain("{{KRUN_ORIGIN}}");
  await expect(page.locator(".sl-markdown-content")).toContainText(
    `krun config set endpoint ${new URL(page.url()).origin}`,
  );
  expect(apiRequests).toEqual([]);
});

test("documents remain readable without JavaScript", async ({ browser }) => {
  const context = await browser.newContext({ javaScriptEnabled: false });
  const page = await context.newPage();
  await page.goto("/docs/");
  await expect(page.getByRole("heading", { name: "2. 安装 CLI" })).toBeVisible();
  await page.getByRole("link", { name: "基础概念", exact: true }).click();
  await expect(page.getByRole("heading", { name: "组织与权限" })).toBeVisible();
  await context.close();
});

test("Starlight works with the control plane content security policy", async ({
  page,
  context,
}) => {
  const violations = [];
  page.on("console", (message) => {
    if (message.type() === "error" && message.text().includes("Content Security Policy")) {
      violations.push(message.text());
    }
  });
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.route("**/docs/", async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      headers: {
        ...response.headers(),
        "content-security-policy":
          "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'",
      },
    });
  });
  await page.goto("/docs/");
  await expect(page.getByRole("heading", { name: "2. 安装 CLI" })).toBeVisible();
  await page.getByRole("button", { name: "复制 Markdown" }).click();
  await expect(page.getByRole("button", { name: "已复制" })).toBeVisible();
  const content = await page.evaluate(() => navigator.clipboard.readText());
  expect(content).toContain(`krun config set endpoint ${new URL(page.url()).origin}`);
  await page.getByRole("button", { name: /搜索/ }).first().click();
  const dialog = page.getByRole("dialog", { name: "搜索" });
  await expect(dialog).toBeVisible();
  await dialog.locator("input").fill("组织");
  await expect(dialog.getByText("基础概念", { exact: false }).first()).toBeVisible();
  expect(violations).toEqual([]);
});

test("mobile navigation and markdown actions are available", async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  await page.goto("/docs/");
  await expect(page.getByRole("link", { name: "查看原文" })).toBeVisible();
  await page.getByRole("button", { name: /菜单/ }).click();
  await expect(page.getByRole("link", { name: "基础概念", exact: true })).toBeVisible();
  await context.close();
});
