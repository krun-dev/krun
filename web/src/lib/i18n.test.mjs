import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { t, locale, savedLanguage, setLanguage } from "./i18n.ts";
import { actionLabel, buildPolicy, initialSelections } from "./permissions.ts";

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
  const zh = JSON.parse(
    fs.readFileSync(new URL("../locales/zh-CN.json", import.meta.url)),
  );
  const en = JSON.parse(
    fs.readFileSync(new URL("../locales/en.json", import.meta.url)),
  );
  assert.deepEqual(Object.keys(zh).sort(), Object.keys(en).sort());
  for (const key of Object.keys(zh)) {
    assert.equal(typeof en[key], "string", key);
    assert.deepEqual(
      zh[key].match(/{{\w+}}/g)?.sort(),
      en[key].match(/{{\w+}}/g)?.sort(),
      key,
    );
    assert.ok(!/[\u3400-\u9fff]/.test(en[key]), key);
  }
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
  assert.equal(
    t("通过 {{name}} 登录", { name: "团队 <script>" }),
    "Sign in with 团队 <script>",
  );
  assert.equal(actionLabel("service.version.read"), "Read Service Version");
  assert.equal(t("Permission denied"), "Permission denied");
  const selections = initialSelections();
  selections.service.actions = ["service.read"];
  assert.deepEqual(
    buildPolicy(selections, ["production"], "org_测试", false).statements[0]
      .actions,
    ["service.read"],
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
  const runtime = JSON.parse(fs.readFileSync(new URL("../../../internal/localize/zh-CN.json", import.meta.url)));
  const keys = new Set([...Object.keys(zh), ...Object.keys(runtime), ...Object.values(runtime)]);
  const files = ["../App.tsx", "../components/common.tsx", "../components/login.tsx", "../components/email-login.tsx", "../components/management.tsx", "../components/keys.tsx", "../components/audit.tsx", "../components/language.tsx", "../components/ui/dialog.tsx", "./api.ts", "./i18n.ts", "./permissions.ts"];
  for (const file of files) {
    const source = fs.readFileSync(new URL(file, import.meta.url), "utf8");
    const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, file.endsWith("tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
    const walk = (node) => {
      if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === "t" && ts.isStringLiteral(node.arguments[0])) {
        assert.ok(keys.has(node.arguments[0].text), `${file}: ${node.arguments[0].text}`);
      }
      ts.forEachChild(node, walk);
    };
    walk(ast);
  }
});
