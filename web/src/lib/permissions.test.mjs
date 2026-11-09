import test from "node:test";
import assert from "node:assert/strict";
import {
  buildPolicy,
  initialSelections,
  keyState,
  parseSelections,
  presetActions,
} from "./permissions.ts";

const environments = ["global", "development", "production"];
const actions = [
  "worker.read",
  "worker.list",
  "worker.version.read",
  "log.read",
  "worker.create",
  "worker.update",
  "worker.delete",
  "worker.publish",
  "worker.rollback",
  "worker.invoke",
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
  selections.worker.actions = ["worker.read"];
  selections.model = {
    actions: ["model.read"],
    all: false,
    ids: "model-a, model-b",
    organization: "",
  };
  const policy = buildPolicy(selections, ["production"], "org-a", false);
  assert.equal(policy.statements.length, 2);
  assert.deepEqual(policy.statements[0].resource, {
    kind: "worker",
    organization_id: "org-a",
    all_in_organization: true,
  });
  assert.deepEqual(policy.statements[1].resource.ids, ["model-a", "model-b"]);
  assert.deepEqual(policy.statements[1].conditions, {
    environments: ["production"],
  });
  const registry = { "worker.read": ["worker"], "model.read": ["model"] };
  const parsed = parseSelections(policy, registry, environments, "org-a", false);
  assert.deepEqual(buildPolicy(parsed.selections, parsed.envs, "org-a", false), policy);
  assert.equal(parseSelections(policy, registry, environments, "org-b", false), null);
});
test("cross-workspace wildcards retain the selected resource kind", () => {
  const selections = initialSelections();
  selections.worker.actions = ["log.read"];
  const policy = buildPolicy(selections, [], "system", true);
  assert.deepEqual(policy.statements[0].resource, {
    kind: "worker",
    all_in_system: true,
  });
  assert.ok(
    parseSelections(policy, { "log.read": ["worker", "model"] }, environments, "system", true),
  );
});
test("JSON conversion refuses rules that would lose restrictions", () => {
  const selections = initialSelections();
  selections.worker.actions = ["worker.read"];
  const policy = buildPolicy(selections, [], "org-a", false);
  const registry = { "worker.read": ["worker"] };
  assert.equal(
    parseSelections(
      { ...policy, statements: [{ ...policy.statements[0], effect: "deny" }] },
      registry,
      environments,
      "org-a",
      false,
    ),
    null,
  );
  assert.equal(
    parseSelections(
      { ...policy, statements: [...policy.statements, ...policy.statements] },
      registry,
      environments,
      "org-a",
      false,
    ),
    null,
  );
  assert.equal(
    parseSelections(
      {
        ...policy,
        statements: [{ ...policy.statements[0], conditions: { future_rule: true } }],
      },
      registry,
      environments,
      "org-a",
      false,
    ),
    null,
  );
  const previewPolicy = buildPolicy(selections, ["feature-preview"], "org-a", false);
  assert.equal(parseSelections(previewPolicy, registry, environments, "org-a", false), null);
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
