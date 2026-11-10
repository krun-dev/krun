import { test, expect } from "@playwright/test";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, resolve } from "node:path";

// Serve the built SPA and authentication fixtures at one origin. No developer
// proxy or production frontend process is involved in these navigation checks.
let server;
let origin;

test.beforeAll(async () => {
  const root = resolve("dist/console");
  server = createServer(async (request, response) => {
    const path = new URL(request.url, "http://localhost").pathname;
    if (path === "/api/auth/providers") {
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(
        JSON.stringify({
          providers: [{ id: "dev", type: "dev", name: "Development" }],
          development: true,
        }),
      );
      return;
    }
    if (path.startsWith("/api/")) {
      response.writeHead(401, { "Content-Type": "application/json" });
      response.end(
        JSON.stringify({ error: { code: "AUTH_REQUIRED", message: "Sign in required" } }),
      );
      return;
    }
    const file = resolve(root, path === "/" ? "index.html" : path.slice(1));
    if (!file.startsWith(root + "/")) {
      response.writeHead(404).end();
      return;
    }
    try {
      const content = await readFile(file);
      const types = { ".html": "text/html", ".css": "text/css", ".js": "text/javascript" };
      response.writeHead(200, {
        "Content-Type": types[extname(file)] || "application/octet-stream",
      });
      response.end(content);
    } catch {
      response.writeHead(404).end();
    }
  });
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  origin = `http://127.0.0.1:${server.address().port}`;
});

test.afterAll(async () => {
  await new Promise((done) => server.close(done));
});

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    if (!localStorage.getItem("krun.language")) {
      localStorage.setItem("krun.language", "zh-CN");
    }
  });
});

test("public home works when the control plane is unavailable", async ({ page }) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/**", (route) =>
    route.fulfill({ status: 503, json: { error: { message: "Unavailable" } } }),
  );
  await page.goto(origin);
  await expect(
    page.getByRole("heading", { level: 1, name: "以更低成本运行网站、API 和全栈应用" }),
  ).toBeVisible();
  await expect(page).toHaveTitle("RUN · Run anything.");
  await expect(page.getByText("为 Agent 打造的 Serverless 计算平台。")).toBeVisible();
  await expect(page.getByText("Agent First", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "查看 Agent 开发指南" })).toHaveAttribute(
    "href",
    "/docs/guides/workers-runtime-compatibility.html",
  );
  await expect(page.getByRole("heading", { name: "Workers", exact: true })).toBeVisible();
  const database = page
    .getByRole("article")
    .filter({ has: page.getByRole("heading", { name: "Database", exact: true }) });
  await expect(database).toContainText("Coming soon");
  await expect(database.getByRole("link")).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0);
  await page.getByRole("button", { name: "静态资源", exact: true }).click();
  await expect(page.getByText("页面与 API，一起部署。", { exact: true })).toBeVisible();
  await expect(page.getByText("index.html", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

for (const [name, section] of [
  ["部署你的第一个应用", "workers"],
  ["管理你的文件", "buckets"],
  ["进入控制台", "login"],
]) {
  test(`home entry preserves the ${section} destination through login`, async ({ page }) => {
    await page.goto(origin);
    await page.getByRole("link", { name, exact: true }).click();
    await expect(page).toHaveURL(`${origin}/?page=${section}`);
    await expect(page.getByRole("heading", { name: "登录组织" })).toBeVisible();
    await expect(page.locator('input[name="return_to"]')).toHaveValue(`/?page=${section}`);
    await page.getByRole("link", { name: "RUN 首页" }).first().click();
    await expect(
      page.getByRole("heading", { level: 1, name: "以更低成本运行网站、API 和全栈应用" }),
    ).toBeVisible();
  });
}

test("home language persists and the mobile layout fits the viewport", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(origin);
  await page.getByRole("combobox", { name: "语言" }).selectOption("en");
  await expect(page.getByRole("link", { name: "Dashboard", exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("combobox", { name: "Language" })).toHaveValue("en");
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Run websites, APIs, and full-stack apps at lower cost",
    }),
  ).toBeVisible();
  await expect(page).toHaveTitle("RUN · Run anything.");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
});
