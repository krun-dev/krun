import test from "node:test";
import assert from "node:assert/strict";
import { APIError } from "./api.ts";
import { nextWorkerPollDelay, pollWorkerOverview } from "./worker-polling.ts";

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

test("active worker polls a compact token and refreshes overview on completion", async () => {
  const calls = [];
  const readOverview = async () => {
    calls.push("overview");
    return { builds: [], activity: { active: false, token: "idle" } };
  };
  let current = { active: true, token: "running" };
  const readActivity = async () => {
    calls.push("activity");
    return current;
  };
  const previous = { active: true, token: "running" };
  assert.equal(await pollWorkerOverview(previous, readActivity, readOverview), undefined);
  assert.deepEqual(calls, ["activity"]);

  current = { active: false, token: "idle" };
  const overview = await pollWorkerOverview(previous, readActivity, readOverview);
  assert.equal(overview.activity.active, false);
  assert.deepEqual(calls, ["activity", "activity", "overview"]);
});

test("an initial or idle worker load reads the full overview directly", async () => {
  const readActivity = () => {
    assert.fail("idle polling must still discover externally started work");
  };
  const readOverview = async () => "overview";
  assert.equal(await pollWorkerOverview(undefined, readActivity, readOverview), "overview");
  assert.equal(
    await pollWorkerOverview({ active: false, token: "idle" }, readActivity, readOverview),
    "overview",
  );
});

test("an active worker falls back to overview when activity is unavailable on an older replica", async () => {
  const result = await pollWorkerOverview(
    { active: true, token: "running" },
    async () => {
      throw new APIError(404, "RESOURCE_NOT_FOUND", "Not found");
    },
    async () => "overview",
  );
  assert.equal(result, "overview");
});

test("activity authorization errors still surface", async () => {
  const denied = new APIError(403, "FORBIDDEN", "Forbidden");
  await assert.rejects(
    pollWorkerOverview(
      { active: true, token: "running" },
      async () => {
        throw denied;
      },
      async () => assert.fail("authorization failures must not fall back"),
    ),
    (error) => error === denied,
  );
});
