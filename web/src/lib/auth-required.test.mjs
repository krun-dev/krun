import test from "node:test";
import assert from "node:assert/strict";
import {
  AUTH_REQUIRED_EVENT,
  notifyAuthRequired,
} from "./auth-required.ts";
import { api, APIError } from "./api.ts";

test("only unauthorized responses request a return to the login page", () => {
  const originalWindow = globalThis.window;
  const browserWindow = new EventTarget();
  globalThis.window = browserWindow;
  let events = 0;
  browserWindow.addEventListener(AUTH_REQUIRED_EVENT, () => events++);

  try {
    notifyAuthRequired(200);
    notifyAuthRequired(403);
    assert.equal(events, 0);

    notifyAuthRequired(401);
    assert.equal(events, 1);
  } finally {
    if (originalWindow === undefined) delete globalThis.window;
    else globalThis.window = originalWindow;
  }
});

test("API 401 responses request login before returning the error", async () => {
  const originalWindow = globalThis.window;
  const originalFetch = globalThis.fetch;
  const browserWindow = new EventTarget();
  globalThis.window = browserWindow;
  globalThis.fetch = async () =>
    new Response(
      JSON.stringify({ error: { code: "UNAUTHENTICATED", message: "请先登录" } }),
      { status: 401, headers: { "Content-Type": "application/json" } },
    );
  let events = 0;
  browserWindow.addEventListener(AUTH_REQUIRED_EVENT, () => events++);

  try {
    await assert.rejects(
      api("/api/v1/orgs"),
      (error) => error instanceof APIError && error.status === 401,
    );
    assert.equal(events, 1);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalWindow === undefined) delete globalThis.window;
    else globalThis.window = originalWindow;
  }
});

test("CSRF 401 responses also request login", async () => {
  const originalWindow = globalThis.window;
  const originalFetch = globalThis.fetch;
  const browserWindow = new EventTarget();
  globalThis.window = browserWindow;
  globalThis.fetch = async () => new Response(null, { status: 401 });
  let events = 0;
  browserWindow.addEventListener(AUTH_REQUIRED_EVENT, () => events++);

  try {
    await assert.rejects(
      api("/api/v1/orgs", "POST", {}),
      (error) => error instanceof APIError && error.status === 401,
    );
    assert.equal(events, 1);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalWindow === undefined) delete globalThis.window;
    else globalThis.window = originalWindow;
  }
});
