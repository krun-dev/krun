import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { t, locale, savedLanguage, setLanguage, roleLabel } from "./i18n.ts";
import { actionLabel, buildPolicy, initialSelections, resourceKinds } from "./permissions.ts";

test("Chinese is the default, including unavailable or invalid storage", () => {
  assert.equal(locale(), "zh-CN");
  assert.equal(t("创建组织"), "创建组织");
  assert.equal(savedLanguage(), "zh-CN");
  globalThis.localStorage = { getItem: () => "invalid", setItem: () => {} };
  assert.equal(savedLanguage(), "zh-CN");
  globalThis.localStorage = {
    getItem: () => {
      throw Error("blocked");
    },
    setItem: () => {
      throw Error("blocked");
    },
  };
  assert.equal(savedLanguage(), "zh-CN");
});

test("catalogs have matching keys and interpolation parameters", () => {
  const zh = JSON.parse(fs.readFileSync(new URL("../locales/zh-CN.json", import.meta.url)));
  const en = JSON.parse(fs.readFileSync(new URL("../locales/en.json", import.meta.url)));
  assert.deepEqual(Object.keys(zh).sort(), Object.keys(en).sort());
  for (const key of Object.keys(zh)) {
    assert.equal(typeof en[key], "string", key);
    assert.deepEqual(zh[key].match(/{{\w+}}/g)?.sort(), en[key].match(/{{\w+}}/g)?.sort(), key);
    assert.ok(!/[\u3400-\u9fff]/.test(en[key]), key);
  }
});

test("organization roles have three clear labels in both languages", async () => {
  await setLanguage("zh-CN");
  assert.deepEqual(["org_admin", "member", "viewer"].map(roleLabel), [
    "组织管理员",
    "成员",
    "只读",
  ]);
  await setLanguage("en");
  assert.deepEqual(["org_admin", "member", "viewer"].map(roleLabel), [
    "Organization administrator",
    "Member",
    "Read only",
  ]);
  assert.equal(roleLabel("system_admin"), "System administrator");
  await setLanguage("zh-CN");
});

test("language switches persist, interpolate data safely, and preserve policy identifiers", async () => {
  let stored;
  globalThis.localStorage = {
    getItem: () => stored,
    setItem: (key, value) => {
      assert.equal(key, "krun.language");
      stored = value;
    },
  };
  await setLanguage("en");
  assert.equal(locale(), "en");
  assert.equal(savedLanguage(), "en");
  assert.equal(t("创建组织"), "Create organization");
  assert.equal(t("{{count}} 个密钥", { count: 1 }), "1 key");
  assert.equal(t("{{count}} 个密钥", { count: 2 }), "2 keys");
  assert.equal(t("通过 {{name}} 登录", { name: "团队 <script>" }), "Sign in with 团队 <script>");
  assert.equal(actionLabel("worker.version.read"), "Read Worker Version");
  assert.equal(actionLabel("model.read"), "Read Model");
  const model = resourceKinds.find(({ kind }) => kind === "model");
  assert.equal(t(model.label), "Model catalog");
  assert.match(t(model.description), /reserved/);
  assert.equal(t("Permission denied"), "Permission denied");
  const selections = initialSelections();
  selections.worker.actions = ["worker.read"];
  assert.deepEqual(
    buildPolicy(selections, ["production"], "org_测试", false).statements[0].actions,
    ["worker.read"],
  );
  await setLanguage("zh-CN");
  assert.equal(t("Create organization"), "创建组织");
  assert.equal(t("Permission denied"), "没有访问权限");
  globalThis.localStorage = {
    setItem: () => {
      throw Error("blocked");
    },
  };
  await setLanguage("en");
  assert.equal(locale(), "en");
  await setLanguage("zh-CN");
});

test("all literal translation keys are cataloged", async () => {
  const ts = await import("typescript");
  const zh = JSON.parse(fs.readFileSync(new URL("../locales/zh-CN.json", import.meta.url)));
  const runtime = JSON.parse(
    fs.readFileSync(new URL("../../../internal/localize/zh-CN.json", import.meta.url)),
  );
  const keys = new Set([...Object.keys(zh), ...Object.keys(runtime), ...Object.values(runtime)]);
  const files = [
    "../App.tsx",
    "../components/common.tsx",
    "../components/login.tsx",
    "../components/home.tsx",
    "../components/oauth-consent.tsx",
    "../components/email-login.tsx",
    "../components/management.tsx",
    "../components/organization-invitations.tsx",
    "../components/organization-invitation-join.tsx",
    "../components/keys.tsx",
    "../components/audit.tsx",
    "../components/workers.tsx",
    "../components/buckets.tsx",
    "../components/bucket-objects.tsx",
    "../components/collection-controls.tsx",
    "../components/hyperdrive.tsx",
    "../components/organization-switcher.tsx",
    "../components/resource-inventory.tsx",
    "../components/system-config.tsx",
    "../components/workers/build-inspector.tsx",
    "../components/workers/domains.tsx",
    "../components/workers/hyperdrive-bindings.tsx",
    "../components/workers/shared.tsx",
    "../components/workers/r2-bindings.tsx",
    "../components/workers/environment.tsx",
    "../components/workers/preview-base.tsx",
    "../components/workers/configurations.tsx",
    "../components/workers/configuration-editor.tsx",
    "../components/workers/configuration-diff.tsx",
    "../components/workers/versions.tsx",
    "../components/workers/version-create.tsx",
    "../components/workers/version-configuration.tsx",
    "../components/workers/version-details.tsx",
    "../components/workers/version.tsx",
    "./object-storage.ts",
    "./worker-variables.ts",
    "../components/workers/variables.tsx",
    "../components/workers/secrets.tsx",
    "../components/workers-repository.tsx",
    "../components/repository-authorizations.tsx",
    "../components/language.tsx",
    "../components/ui/dialog.tsx",
    "./api.ts",
    "./i18n.ts",
    "./permissions.ts",
  ];
  for (const file of files) {
    const source = fs.readFileSync(new URL(file, import.meta.url), "utf8");
    const ast = ts.createSourceFile(
      file,
      source,
      ts.ScriptTarget.Latest,
      true,
      file.endsWith("tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
    );
    const walk = (node) => {
      if (
        ts.isCallExpression(node) &&
        ts.isIdentifier(node.expression) &&
        node.expression.text === "t" &&
        ts.isStringLiteral(node.arguments[0])
      ) {
        assert.ok(keys.has(node.arguments[0].text), `${file}: ${node.arguments[0].text}`);
      }
      ts.forEachChild(node, walk);
    };
    walk(ast);
  }
});
