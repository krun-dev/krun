import test from "node:test";
import assert from "node:assert/strict";
import {
  configurationDifferences,
  confirmedConfigurationVersion,
} from "./worker-configurations.ts";

test("configuration differences include deletions, JSON changes and storage bindings", () => {
  const current = {
    variables: { TEXT: "1", JSON: { b: 2, a: 1 }, REMOVE: true },
    r2_buckets: { UPLOADS: { bucket_name: "old", bucket_id: "a" } },
  };
  const selected = {
    variables: { TEXT: 1, JSON: { a: 1, b: 2 }, ADD: false },
    r2_buckets: { UPLOADS: { bucket_name: "new", bucket_id: "b" } },
    version_metadata: "INFO",
  };
  const rows = configurationDifferences(selected, current);
  assert.equal(rows.find((r) => r.name === "TEXT").changed, true);
  assert.equal(rows.find((r) => r.name === "JSON").changed, false);
  assert.equal(rows.find((r) => r.name === "REMOVE").after, "—");
  assert.equal(rows.find((r) => r.name === "ADD").before, "—");
  assert.equal(rows.find((r) => r.name === "UPLOADS · R2").changed, true);
  assert.equal(rows.find((r) => r.name === "version_metadata").after, "INFO");
});

test("a queued or committed version never replaces the confirmed configuration marker", () => {
  const old = { id: "confirmed", configuration_id: "baseline" };
  const next = { id: "next", configuration_id: "candidate" };
  const versions = { confirmed: old, next };
  assert.equal(
    confirmedConfigurationVersion(
      { version_id: "next", confirmed_version_id: "confirmed" },
      versions,
    ),
    old,
  );
  assert.equal(confirmedConfigurationVersion({ version_id: "next" }, versions), undefined);
});
