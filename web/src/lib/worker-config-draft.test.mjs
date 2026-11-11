import test from "node:test";
import assert from "node:assert/strict";
import {
  draftRows,
  environmentConfigPatch,
  hyperdriveInput,
  hyperdriveRows,
} from "./worker-config-draft.ts";

test("hyperdrive rows round-trip bindings", () => {
  const rows = hyperdriveRows({ DB: "hd-1", CACHE: "hd-2" });
  assert.deepEqual(rows, [
    { name: "DB", id: "hd-1" },
    { name: "CACHE", id: "hd-2" },
  ]);
  assert.deepEqual(hyperdriveInput(rows), { DB: "hd-1", CACHE: "hd-2" });
});

test("hyperdrive input trims names and drops incomplete rows", () => {
  assert.deepEqual(
    hyperdriveInput([
      { name: " DB ", id: "hd-1" },
      { name: "  ", id: "hd-2" },
      { name: "CACHE", id: "" },
    ]),
    { DB: "hd-1" },
  );
});

test("draft rows normalize variables, bindings and metadata", () => {
  const rows = draftRows({
    variables: { TEXT: "value" },
    r2_buckets: { UPLOADS: { bucket_id: "b", bucket_name: "uploads" } },
    hyperdrive_bindings: { DB: "hd-1" },
    version_metadata: "CF_VERSION",
    secret_names: ["TOKEN"],
  });
  assert.equal(rows.variables.length, 1);
  assert.equal(rows.variables[0].name, "TEXT");
  assert.equal(rows.r2Bindings.length, 1);
  assert.deepEqual(rows.hyperdriveBindings, [{ name: "DB", id: "hd-1" }]);
  assert.equal(rows.versionMetadata, "CF_VERSION");
  assert.deepEqual(rows.secretNames, ["TOKEN"]);
});

test("environment configuration saves only the edited section", () => {
  const saved = { variables: { SAVED: "value" }, secret_names: ["TOKEN"] };
  const draft = {
    variables: [{ id: "var", name: "UPLOADS", kind: "text", value: "unsaved" }],
    versionMetadata: "",
    r2Bindings: [{ id: "r2", name: "UPLOADS", bucket: "uploads" }],
    hyperdriveBindings: [],
  };
  const payload = (section) =>
    JSON.parse(JSON.stringify(environmentConfigPatch(section, saved, draft)));

  assert.deepEqual(payload("variables"), {
    variables: { UPLOADS: "unsaved" },
    version_metadata: "",
  });
  assert.deepEqual(payload("r2"), { r2_buckets: { UPLOADS: "uploads" } });
  assert.deepEqual(payload("hyperdrive"), { hyperdrive_bindings: {} });
  assert.throws(() =>
    environmentConfigPatch("r2", saved, {
      ...draft,
      r2Bindings: [{ id: "r2", name: "TOKEN", bucket: "uploads" }],
    }),
  );
});
