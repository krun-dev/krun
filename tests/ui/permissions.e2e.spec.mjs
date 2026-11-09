import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

// Real static SPA, session cookies/CSRF, Go and PostgreSQL. Run through
// scripts/test-object-browser.sh, which provides isolated identities and data.
test.skip(!process.env.KRUN_IAM_E2E, "Requires the isolated IAM browser fixture");
test.describe.configure({ mode: "serial" });

let admin;
let member;
let viewer;
let organization;
let viewerID;
const errors = [];
const orgPath = () => `/api/v1/orgs/${organization.id}`;

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

async function login(browser, provider) {
  const context = await browser.newContext({ baseURL: process.env.KRUN_UI_ORIGIN });
  await context.addInitScript(() => localStorage.setItem("krun.language", "zh-CN"));
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/?page=login");
  await page.locator(`form[action="/api/auth/${provider}/login"]`).getByRole("button").click();
  await expect(page.getByRole("button", { name: "退出登录", exact: true })).toBeVisible();
  return { context, page };
}

test.beforeAll(async ({ browser }) => {
  admin = await login(browser, "ui-dev");
  member = await login(browser, "ui-member");
  viewer = await login(browser, "ui-viewer");
});
test.afterEach(() => expect(errors).toEqual([]));
test.afterAll(async () => {
  await Promise.all([admin?.context.close(), member?.context.close(), viewer?.context.close()]);
});

test("ordinary users create their own organization without gaining system or cross-organization access", async () => {
  const page = member.page;
  await expect(page.getByRole("heading", { name: "你还没有加入任何组织" })).toBeVisible();
  await expect(page.locator("aside nav button")).toHaveText(["概览"]);
  expect((await api(member.context, "/api/auth/me")).system_admin).toBe(false);

  await page.getByRole("button", { name: "创建组织", exact: true }).click();
  const name = "Self-service IAM " + randomUUID().slice(0, 8);
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "组织名称", exact: true }).fill(name);
  await dialog.getByRole("button", { name: "创建组织", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("heading", { name: "组织概览", exact: true })).toBeVisible();
  organization = (await api(member.context, "/api/v1/orgs")).items.find((org) => org.name === name);
  expect(organization).toBeTruthy();
  const permissions = await api(member.context, orgPath() + "/permissions");
  expect(permissions.actions["iam.read"]).toBe(true);
  expect(permissions.actions["storage.object.read"]).toBe(true);
  expect(permissions.actions["audit.read"]).toBe(true);
  await api(member.context, orgPath() + "/audit");
  await expect(
    page.locator("aside nav").getByRole("button", { name: "组织成员", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "平台管理", exact: true })).toHaveCount(0);
  await api(member.context, "/api/v1/system/users", "GET", undefined, 403);
  await api(member.context, "/api/v1/system/role-assignments", "GET", undefined, 403);

  const other = await api(
    admin.context,
    "/api/v1/orgs",
    "POST",
    { name: "Other isolated team" },
    201,
  );
  await api(member.context, `/api/v1/orgs/${other.id}/permissions`, "GET", undefined, 404);
  await api(member.context, `/api/v1/orgs/${other.id}/members`, "GET", undefined, 403);
  expect((await api(member.context, "/api/auth/me")).system_admin).toBe(false);
});

test("viewer permissions drive navigation and cannot be bypassed through URLs or API requests", async () => {
  viewerID = (await api(viewer.context, "/api/auth/me")).user.user_id;
  await api(admin.context, orgPath() + "/members", "POST", { user_id: viewerID }, 201);
  await api(
    admin.context,
    orgPath() + "/role-assignments",
    "POST",
    {
      subject_kind: "user",
      subject_id: viewerID,
      role_id: "viewer",
      scope_kind: "organization",
      scope_id: organization.id,
      conditions: {},
    },
    201,
  );
  const page = viewer.page;
  await page.goto(`/?page=workers&workspace=${organization.id}`);
  await expect(page.getByRole("heading", { name: "Workers", exact: true })).toBeVisible();
  await expect(page.locator("aside nav button")).toHaveText([
    "概览",
    "Workers",
    "对象存储",
    "API 密钥",
    "权限检查",
  ]);
  await expect(page.getByRole("button", { name: "创建 Worker", exact: true })).toHaveCount(0);
  const permissions = await api(viewer.context, orgPath() + "/permissions");
  expect(permissions.actions["worker.list"]).toBe(true);
  expect(permissions.actions["iam.read"]).toBe(false);
  expect(permissions.actions["storage.object.read"]).toBe(true);
  expect(permissions.actions["storage.object.write"]).toBe(false);

  const bucket = await api(
    admin.context,
    orgPath() + "/buckets",
    "POST",
    { name: "viewer-" + randomUUID().slice(0, 8) },
    201,
  );
  await page.goto(`/?page=buckets&workspace=${organization.id}`);
  await expect(page.getByRole("button", { name: "创建桶", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "停用", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: bucket.name, exact: true }).click();
  await expect(page.getByText("目录为空", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "上传文件", exact: true })).toHaveCount(0);
  await api(viewer.context, orgPath() + "/buckets", "POST", { name: "cannot-create" }, 403);
  await api(
    viewer.context,
    `${orgPath()}/buckets/${bucket.id}/objects/access`,
    "POST",
    { operation: "upload", key: "denied.txt", size: 1 },
    403,
  );

  for (const section of ["members", "roles", "audit", "users"]) {
    await page.goto(`/?page=${section}&workspace=${organization.id}&view=system`);
    await expect(page.getByRole("heading", { name: "组织概览", exact: true })).toBeVisible();
  }
  for (const path of [
    orgPath() + "/members",
    orgPath() + "/role-assignments",
    "/api/v1/system/users",
  ]) {
    await api(viewer.context, path, "GET", undefined, 403);
  }
  await api(
    viewer.context,
    orgPath() + "/role-assignments",
    "POST",
    {
      subject_kind: "user",
      subject_id: viewerID,
      role_id: "org_admin",
      scope_kind: "organization",
      scope_id: organization.id,
      conditions: {},
    },
    403,
  );
});

test("the role picker offers three roles and a member can work without organization management", async () => {
  const roles = await api(admin.context, "/api/v1/roles");
  expect(roles.roles).toEqual(["system_admin", "org_admin", "member", "viewer"]);
  const ownerPage = member.page;
  await ownerPage.goto(`/?page=roles&workspace=${organization.id}`);
  await ownerPage.getByRole("button", { name: "授予角色", exact: true }).click();
  const dialog = ownerPage.getByRole("dialog");
  const roleSelect = dialog.getByRole("combobox", { name: "角色", exact: true });
  await expect(roleSelect.locator("option")).toHaveText([
    "组织管理员 (org_admin)",
    "成员 (member)",
    "只读 (viewer)",
  ]);
  await dialog.getByRole("textbox", { name: /本地用户 ID/ }).fill(viewerID);
  await roleSelect.selectOption("member");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const grants = await api(member.context, orgPath() + "/role-assignments");
  const grant = grants.items.find((item) => item.subject_id === viewerID && item.role_id === "member");
  expect(grant).toBeTruthy();
  const permissions = await api(viewer.context, orgPath() + "/permissions");
  expect(permissions.actions["worker.create"]).toBe(true);
  expect(permissions.actions["storage.bucket.create"]).toBe(true);
  expect(permissions.actions["iam.grant"]).toBe(false);
  expect(permissions.actions["organization.update"]).toBe(false);
  await viewer.page.goto(`/?page=workers&workspace=${organization.id}`);
  await expect(viewer.page.getByRole("button", { name: "创建 Worker", exact: true })).toBeVisible();
  await expect(viewer.page.locator("aside nav").getByRole("button", { name: "组织成员", exact: true })).toHaveCount(0);
  await api(viewer.context, orgPath() + "/members", "GET", undefined, 403);
  await api(viewer.context, "/api/v1/system/users", "GET", undefined, 403);
  const resource = await api(viewer.context, orgPath() + "/workers", "POST", { name: "member-resource" }, 201);
  await api(viewer.context, `${orgPath()}/workers/${resource.id}`, "DELETE", undefined, 204);
  const bucket = await api(viewer.context, orgPath() + "/buckets", "POST", { name: "member-" + randomUUID().slice(0, 8) }, 201);
  await api(viewer.context, `${orgPath()}/buckets/${bucket.id}`, "PATCH", { enabled: false, revision: bucket.revision });
  await viewer.page.goto(`/?page=buckets&workspace=${organization.id}`);
  await expect(viewer.page.getByRole("button", { name: "创建桶", exact: true })).toBeVisible();

  await api(member.context, `${orgPath()}/role-assignments/${grant.assignment_id}`, "DELETE", undefined, 204);
  await viewer.page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(viewer.page.getByRole("button", { name: "创建桶", exact: true })).toHaveCount(0);
  await api(viewer.context, orgPath() + "/workers", "POST", { name: "write-after-revoke" }, 403);
  for (const role of ["developer", "publisher"]) {
    await api(member.context, orgPath() + "/role-assignments", "POST", {
      subject_kind: "user", subject_id: viewerID, role_id: role,
      scope_kind: "organization", scope_id: organization.id, conditions: {},
    }, 422);
  }
});

test("group grants and revocations refresh the live page against PostgreSQL", async () => {
  const group = await api(
    admin.context,
    orgPath() + "/groups",
    "POST",
    { name: "Temporary operators" },
    201,
  );
  await api(admin.context, `${orgPath()}/groups/${group.id}/members/${viewerID}`, "PUT", {});
  const grant = await api(
    admin.context,
    orgPath() + "/role-assignments",
    "POST",
    {
      subject_kind: "group",
      subject_id: group.id,
      role_id: "org_admin",
      scope_kind: "organization",
      scope_id: organization.id,
      conditions: {},
    },
    201,
  );
  const page = viewer.page;
  await page.reload();
  await page.locator("aside nav").getByRole("button", { name: "组织成员", exact: true }).click();
  await expect(page.getByRole("heading", { name: "组织成员", exact: true })).toBeVisible();
  expect((await api(viewer.context, orgPath() + "/permissions")).actions["iam.read"]).toBe(true);

  await api(
    admin.context,
    `${orgPath()}/role-assignments/${grant.assignment_id}`,
    "DELETE",
    undefined,
    204,
  );
  await api(viewer.context, orgPath() + "/members", "GET", undefined, 403);
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(page.getByRole("heading", { name: "组织概览", exact: true })).toBeVisible();
  await expect(
    page.locator("aside nav").getByRole("button", { name: "组织成员", exact: true }),
  ).toHaveCount(0);
  expect((await api(viewer.context, orgPath() + "/permissions")).actions["iam.read"]).toBe(false);
});
