import test from "node:test";
import assert from "node:assert/strict";
import {
  captureOrganizationInvitation,
  clearOrganizationInvitation,
} from "./organization-invitations.ts";

const token = "krun_invite_" + "x".repeat(43);
const key = "krun.organization-invitation";

function browser(url) {
  const values = new Map();
  globalThis.window = { location: { href: url } };
  globalThis.history = {
    replaceState(_state, _title, next) {
      window.location.href = String(next);
    },
  };
  globalThis.sessionStorage = {
    getItem(name) {
      return values.get(name) ?? null;
    },
    setItem(name, value) {
      values.set(name, value);
    },
    removeItem(name) {
      values.delete(name);
    },
  };
  return values;
}

test("capture removes the capability from the URL and survives a same-tab login return", () => {
  const values = browser("https://console.example.com/?page=join#invite=" + token);
  assert.deepEqual(captureOrganizationInvitation(), { token, error: null });
  assert.equal(new URL(window.location.href).hash, "");
  assert.equal(new URL(window.location.href).search, "?page=join");
  assert.equal(JSON.parse(values.get(key)).token, token);
  assert.deepEqual(captureOrganizationInvitation(), { token, error: null });
  clearOrganizationInvitation();
  assert.equal(values.has(key), false);
});

test("a new invitation replaces a pending invitation and invalid tokens clear it", () => {
  const values = browser("https://console.example.com/?page=join#invite=" + token);
  captureOrganizationInvitation();
  const nextToken = "krun_invite_" + "y".repeat(43);
  window.location.href = "https://console.example.com/?page=join#invite=" + nextToken;
  assert.equal(captureOrganizationInvitation().token, nextToken);
  window.location.href = "https://console.example.com/?page=join#invite=invalid";
  assert.deepEqual(captureOrganizationInvitation(), { token: "", error: "missing" });
  assert.equal(values.has(key), false);
});

test("missing, malformed and stale stored invitations do not become credentials", () => {
  const values = browser("https://console.example.com/?page=join");
  assert.equal(captureOrganizationInvitation().error, "missing");
  for (const value of [
    "not-json",
    JSON.stringify({ token, saved_at: 1 }),
    JSON.stringify({ token: "invalid", saved_at: Date.now() }),
  ]) {
    values.set(key, value);
    assert.equal(captureOrganizationInvitation().error, "missing");
    assert.equal(values.has(key), false);
  }
});

test("blocked session storage fails explicitly without losing the supplied link", () => {
  browser("https://console.example.com/?page=join#invite=" + token);
  sessionStorage.setItem = () => {
    throw new Error("blocked");
  };
  assert.deepEqual(captureOrganizationInvitation(), { token: "", error: "storage" });
  assert.equal(new URL(window.location.href).hash, "#invite=" + token);
});
