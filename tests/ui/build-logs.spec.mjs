import { test, expect } from "@playwright/test";

// Run against Vite: KRUN_UI_ORIGIN=http://127.0.0.1:4391 npx playwright test tests/ui/build-logs.spec.mjs
// This verifies the shared transport used by the log dialog in a real browser.
test("build log transport reads signed objects and obeys the poll header", async ({ page }) => {
  let calls = 0;
  let objectRequests = 0;
  await page.route("**/api/**", async (route) => {
    if (!route.request().url().endsWith("/builds/build/logs")) {
      await route.fulfill({ status: 401, json: { error: { code: "UNAUTHENTICATED" } } });
      return;
    }
    calls += 1;
    await route.fulfill({
      json: { state: calls === 1 ? "building" : "succeeded", download_url: "https://logs.example.test/build.txt?signature=test" },
      headers: calls === 1 ? { "X-Log-Poll-Interval": "10" } : {},
    });
  });
  await page.route("https://logs.example.test/**", async (route) => {
    objectRequests += 1;
    const headers = route.request().headers();
    expect(headers.authorization).toBeUndefined();
    expect(headers.cookie).toBeUndefined();
    await route.fulfill({ body: objectRequests === 1 ? "building\n" : "building\nfinished\n", headers: { "Access-Control-Allow-Origin": "*" } });
  });
  await page.goto("/");
  const read = () => page.evaluate(async () => {
    const { readBuildLog } = await import("/src/lib/build-logs.ts");
    return readBuildLog("/api/v1/orgs/org/services/svc/workers/builds/build/logs");
  });
  expect(await read()).toEqual({ state: "building", log: "building\n", pollSeconds: 10 });
  expect(await read()).toEqual({ state: "succeeded", log: "building\nfinished\n", pollSeconds: null });
  expect(objectRequests).toBe(2);
});
