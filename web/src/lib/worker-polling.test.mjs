import test from "node:test";
import assert from "node:assert/strict";
import { nextWorkerPollDelay } from "./worker-polling.ts";

test("worker polling is quick only while work is active", () => {
  assert.equal(nextWorkerPollDelay(true, 0), 1200);
  assert.equal(nextWorkerPollDelay(false, 0), 15000);
});

test("worker polling backs off repeated failures with a cap", () => {
  assert.equal(nextWorkerPollDelay(true, 1), 5000);
  assert.equal(nextWorkerPollDelay(false, 2), 10000);
  assert.equal(nextWorkerPollDelay(true, 8), 60000);
  assert.equal(nextWorkerPollDelay(true, 100), 60000);
});
