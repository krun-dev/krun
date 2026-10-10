import test from "node:test";
import assert from "node:assert/strict";
import {
  buildPolicy,
  initialSelections,
  keyState,
  parseSelections,
  presetActions,
} from "./permissions.ts";

const actions = [
  "service.read",
  "service.list",
  "service.version.read",
  "log.read",
  "service.create",
  "service.update",
  "service.delete",
  "service.publish",
  "service.rollback",
  "service.invoke",
  "build.start",
];
test("read/write presets do not include execution or deployment", () => {
  assert.deepEqual(presetActions(actions, "read"), actions.slice(0, 4));
  assert.deepEqual(presetActions(actions, "readwrite"), actions.slice(0, 7));
  assert.deepEqual(presetActions(actions, "all"), actions);
  assert.deepEqual(presetActions(actions, "clear"), []);
});
test("multiple resource types retain their own selectors and conditions", () => {
  const selections = initialSelections();
  selections.service.actions = ["service.read"];
  selections.model = {
    actions: ["model.read"],
    all: false,
    ids: "model-a, model-b",
    organization: "",
  };
  const policy = buildPolicy(selections, ["staging"], "org-a", false);
  assert.equal(policy.statements.length, 2);
  assert.deepEqual(policy.statements[0].resource, {
    kind: "service",
    organization_id: "org-a",
    all_in_organization: true,
  });
  assert.deepEqual(policy.statements[1].resource.ids, ["model-a", "model-b"]);
  assert.deepEqual(policy.statements[1].conditions, {
    environments: ["staging"],
  });
  const registry = { "service.read": ["service"], "model.read": ["model"] };
  assert.deepEqual(
    buildPolicy(
      parseSelections(policy, registry, "org-a", false).selections,
      ["staging"],
      "org-a",
      false,
    ),
    policy,
  );
  assert.equal(parseSelections(policy, registry, "org-b", false), null);
});
test("cross-workspace wildcards retain the selected resource kind", () => {
  const selections = initialSelections();
  selections.service.actions = ["log.read"];
  const policy = buildPolicy(selections, [], "system", true);
  assert.deepEqual(policy.statements[0].resource, {
    kind: "service",
    all_in_system: true,
  });
  assert.ok(
    parseSelections(
      policy,
      { "log.read": ["service", "model"] },
      "system",
      true,
    ),
  );
});
test("JSON conversion refuses rules that would lose restrictions", () => {
  const selections = initialSelections();
  selections.service.actions = ["service.read"];
  const policy = buildPolicy(selections, [], "org-a", false);
  const registry = { "service.read": ["service"] };
  assert.equal(
    parseSelections(
      { ...policy, statements: [{ ...policy.statements[0], effect: "deny" }] },
      registry,
      "org-a",
      false,
    ),
    null,
  );
  assert.equal(
    parseSelections(
      { ...policy, statements: [...policy.statements, ...policy.statements] },
      registry,
      "org-a",
      false,
    ),
    null,
  );
  assert.equal(
    parseSelections(
      {
        ...policy,
        statements: [
          { ...policy.statements[0], conditions: { future_rule: true } },
        ],
      },
      registry,
      "org-a",
      false,
    ),
    null,
  );
});
test("permanent keys stay active while revocation and finite deadlines win", () => {
  assert.equal(keyState({ revoked: false, expires_at: null }), "active");
  assert.equal(keyState({ revoked: true, expires_at: null }), "revoked");
  assert.equal(
    keyState(
      { revoked: false, expires_at: "2026-10-09T00:00:00Z" },
      Date.parse("2026-10-09T00:00:00Z"),
    ),
    "expired",
  );
});
