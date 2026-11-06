import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

test.skip(!process.env.KRUN_IAM_E2E, "Requires the isolated IAM browser fixture");
test.describe.configure({ mode: "serial" });
test.use({ actionTimeout: 20_000 });

let admin;
let member;
let outsider;
let organization;
let invitation;
let memberID;
const errors = [];
const endpoint = () => `/api/v1/orgs/${organization.id}/invitation-links`;

async function api(context, path, method = "GET", data, expected = 200) {
  const headers = { origin: new URL(process.env.KRUN_UI_ORIGIN).origin };
  if (method !== "GET") {
    const csrf = await context.request.post("/api/auth/csrf", { headers });
    expect(csrf.ok()).toBeTruthy();
    headers["X-CSRF-Token"] = (await csrf.json()).csrf_token;
  }
  const response = await context.request.fetch(path, { method, headers, data });
  expect(response.status(), `${method} ${path}: ${await response.text()}`).toBe(expected);
  return response.status() === 204 ? undefined : response.json();
}

async function contextPage(browser) {
  const context = await browser.newContext({ baseURL: process.env.KRUN_UI_ORIGIN });
  await context.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  return { context, page };
}

async function login(browser, provider) {
  const client = await contextPage(browser);
  await client.page.goto("/");
  await client.page
    .locator(`form[action="/api/auth/${provider}/login"]`)
    .getByRole("button")
    .click();
  await expect(client.page.getByRole("button", { name: "退出登录", exact: true })).toBeVisible();
  return client;
}

const tokenOf = (url) => new URL(url).hash.slice("#invite=".length);

test.beforeAll(async ({ browser }) => {
  admin = await login(browser, "ui-dev");
  const temporary = await login(browser, "ui-invitee");
  memberID = (await api(temporary.context, "/api/auth/me")).user.user_id;
  await temporary.context.close();
  outsider = await login(browser, "ui-outsider");
  member = await contextPage(browser);
  organization = await api(
    admin.context,
    "/api/v1/orgs",
    "POST",
    {
      name: "Invitation browser " + randomUUID().slice(0, 8),
    },
    201,
  );
});
test.afterEach(() => expect(errors).toEqual([]));
test.afterAll(async () => {
  await Promise.all([admin?.context.close(), member?.context.close(), outsider?.context.close()]);
});

test("create a directed, single-use link in the UI and protect its recipient", async () => {
  const page = admin.page;
  await page.goto(`/?page=members&workspace=${organization.id}`);
  await page.getByRole("button", { name: "邀请成员", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("radio", { name: /^成员/ })).toBeChecked();
  await dialog.getByText("更多设置", { exact: true }).click();
  await expect(dialog.getByRole("spinbutton", { name: /^最多加入人数/ })).toHaveValue("1");
  await dialog.getByRole("combobox", { name: "邀请对象", exact: true }).selectOption("user");
  await dialog.getByRole("textbox", { name: /^受邀用户 ID/ }).fill(memberID);
  await dialog.getByRole("button", { name: "生成邀请链接", exact: true }).click();
  await expect(dialog.getByRole("heading", { name: "邀请链接已生成", exact: true })).toBeVisible();
  const url = await dialog.getByRole("textbox", { name: "邀请链接", exact: true }).inputValue();
  const items = (await api(admin.context, endpoint())).items;
  invitation = { ...items.find((item) => item.target_user_id === memberID), url };
  expect(invitation.max_uses).toBe(1);
  expect(invitation.role_id).toBe("member");
  expect(invitation.token_hash).toBeUndefined();
  await page.keyboard.press("Escape");
  await page.getByRole("tab", { name: "邀请链接", exact: true }).click();
  await expect(page.getByText(memberID, { exact: true })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "邀请链接", exact: true })).toHaveCount(0);
  const preview = await api(member.context, "/api/v1/invitation-links/preview", "POST", {
    token: tokenOf(url),
  });
  expect(preview.targeted).toBe(true);
  expect(JSON.stringify(preview)).not.toContain(memberID);
  await api(
    outsider.context,
    "/api/v1/invitation-links/accept",
    "POST",
    { token: tokenOf(url) },
    403,
  );
  expect((await api(admin.context, endpoint())).items[0].use_count).toBe(0);
});

test("anonymous landing survives login without leaking the token, then joins on explicit confirmation", async () => {
  const page = member.page;
  const requestURLs = [];
  page.on("request", (request) => requestURLs.push(request.url()));
  await page.goto(invitation.url);
  await expect(page.getByRole("heading", { name: "加入组织", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "登录后加入", exact: true })).toBeVisible();
  await expect(page).toHaveURL(/\?page=join$/);
  await page.getByRole("button", { name: "登录后加入", exact: true }).click();
  await page.locator('form[action="/api/auth/ui-invitee/login"]').getByRole("button").click();
  await expect(page.getByRole("button", { name: "加入组织", exact: true })).toBeVisible();
  expect((await api(admin.context, endpoint())).items[0].use_count).toBe(0);
  await page.getByRole("button", { name: "加入组织", exact: true }).click();
  await expect(page.getByRole("heading", { name: "已加入组织", exact: true })).toBeVisible();
  expect(
    await page.evaluate(() => sessionStorage.getItem("krun.organization-invitation")),
  ).toBeNull();
  expect(requestURLs.every((url) => !url.includes(tokenOf(invitation.url)))).toBe(true);
  expect((await api(admin.context, endpoint())).items[0].use_count).toBe(1);
  const assignments = await api(admin.context, `/api/v1/orgs/${organization.id}/role-assignments`);
  expect(
    assignments.items.filter((item) => item.subject_id === memberID).map((item) => item.role_id),
  ).toEqual(["member"]);
  await page.getByRole("button", { name: "进入组织", exact: true }).click();
  await expect(page.getByRole("heading", { name: "组织概览", exact: true })).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`workspace=${organization.id}`));
});

test("existing membership keeps its role and quota; revocation disables the landing page", async () => {
  const created = await api(admin.context, endpoint(), "POST", { role_id: "viewer" }, 201);
  await member.page.goto(created.invite_url);
  await member.page.getByRole("button", { name: "加入组织", exact: true }).click();
  await expect(
    member.page.getByRole("heading", { name: "你已是组织成员", exact: true }),
  ).toBeVisible();
  await expect(
    member.page.getByText("本次邀请不会修改你当前的角色。", { exact: true }),
  ).toBeVisible();
  const item = (await api(admin.context, endpoint())).items.find(
    (entry) => entry.id === created.invitation.id,
  );
  expect(item.use_count).toBe(0);
  const page = admin.page;
  await page.reload();
  await page.getByRole("tab", { name: "邀请链接", exact: true }).click();
  await page
    .getByRole("row")
    .filter({ hasText: "0 / 1" })
    .getByRole("button", { name: "撤销", exact: true })
    .click();
  await page.getByRole("dialog").getByRole("button", { name: "确认撤销", exact: true }).click();
  await expect(page.getByText("已撤销", { exact: true })).toBeVisible();
  await member.page.goto(created.invite_url);
  await expect(member.page.getByRole("alert")).toContainText("邀请链接已失效");
  await expect(member.page.getByRole("button", { name: "加入组织", exact: true })).toHaveCount(0);
});

test("a shared one-person link admits exactly one of two accounts", async () => {
  const org = await api(
    admin.context,
    "/api/v1/orgs",
    "POST",
    { name: "Quota browser " + randomUUID().slice(0, 8) },
    201,
  );
  const created = await api(
    admin.context,
    `/api/v1/orgs/${org.id}/invitation-links`,
    "POST",
    {},
    201,
  );
  const token = tokenOf(created.invite_url);
  const accept = async (client) => {
    const headers = { origin: new URL(process.env.KRUN_UI_ORIGIN).origin };
    headers["X-CSRF-Token"] = (
      await (await client.context.request.post("/api/auth/csrf", { headers })).json()
    ).csrf_token;
    return client.context.request.post("/api/v1/invitation-links/accept", {
      headers,
      data: { token },
    });
  };
  const results = await Promise.all([accept(member), accept(outsider)]);
  expect(results.map((response) => response.status()).sort()).toEqual([200, 410]);
  expect(
    (await api(admin.context, `/api/v1/orgs/${org.id}/invitation-links`)).items[0].use_count,
  ).toBe(1);
});
