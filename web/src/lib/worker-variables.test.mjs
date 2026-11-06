import test from "node:test";
import assert from "node:assert/strict";
import { variableRows, variableInput } from "./worker-variables.ts";

test("variable rows preserve text and all JSON value types across an edit", () => {
  const input = {
    TOKEN_LIKE_TEXT: "false",
    JSON_LIKE_TEXT: '{"enabled":true}',
    EMPTY: "",
    COUNT: 7,
    FLAG: false,
    CONFIG: { nested: [1, true, null] },
    LIST: ["a", 2],
    NULL: null,
  };
  const rows = variableRows(input);
  assert.equal(rows.find((row) => row.name === "TOKEN_LIKE_TEXT").kind, "text");
  assert.equal(rows.find((row) => row.name === "COUNT").kind, "json");
  assert.deepEqual({ ...variableInput(rows) }, input);
});

test("variable validation rejects duplicate or reserved names and invalid JSON", () => {
  for (const rows of [
    [{ name: "", kind: "text", value: "x" }],
    [{ name: "KRUN_TOKEN", kind: "text", value: "x" }],
    [{ name: "bad name", kind: "text", value: "x" }],
    [{ name: "CONFIG", kind: "json", value: "{" }],
    [
      { name: " NAME ", kind: "text", value: "x" },
      { name: "NAME", kind: "text", value: "y" },
    ],
  ]) {
    assert.throws(() => variableInput(rows));
  }
});

test("variable names do not mutate the result prototype", () => {
  const variables = variableInput([
    { name: "__proto__", kind: "json", value: '{"injected":true}' },
  ]);
  assert.equal(Object.getPrototypeOf(variables), null);
  assert.equal(Object.hasOwn(variables, "__proto__"), true);
  assert.equal({}.injected, undefined);
  assert.equal(JSON.stringify(variables), '{"__proto__":{"injected":true}}');
});
