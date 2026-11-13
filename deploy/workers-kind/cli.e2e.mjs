import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { request } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";

function loopbackOrigin(value, name) {
  assert(value, name + " is required");
  const url = new URL(value);
  assert.equal(url.protocol + "//" + url.hostname, "http://127.0.0.1", name);
  assert.equal(url.href, url.origin + "/", name);
  return url.origin;
}

const origin = loopbackOrigin(process.env.KRUN_UI_ORIGIN, "KRUN_UI_ORIGIN");
const gateway = loopbackOrigin(
  process.env.KRUN_CLI_E2E_GATEWAY_ORIGIN,
  "KRUN_CLI_E2E_GATEWAY_ORIGIN",
);
const token = process.env.KRUN_TOKEN;
assert(token?.startsWith("krun_key_"), "Supply a dedicated kind API Key in KRUN_TOKEN");
const org = process.env.KRUN_CLI_E2E_ORG;
assert(org, "Supply a dedicated kind organization in KRUN_CLI_E2E_ORG");
assert((await fetch(origin + "/api/healthz")).ok, "kind backend must be healthy");

const dir = mkdtempSync(path.join(tmpdir(), "krun-cli-e2e-"));
const binary = path.join(dir, "krun");
const profile = path.join(dir, "profile.yaml");
const run = randomUUID().slice(0, 8);
let worker;
let scope;
let checks = 0;

function command(args, input) {
  const result = spawnSync(binary, ["--no-update-check", "--profile", profile, ...args], {
    encoding: "utf8",
    env: { ...process.env, KRUN_TOKEN: "", KRUN_ENDPOINT: origin },
    input,
    timeout: 180000,
  });
  if (result.error) throw result.error;
  return result;
}

function cli(...args) {
  const result = command(args);
  assert.equal(result.status, 0, args.join(" ") + "\n" + result.stderr);
  return result.stdout.trim() ? JSON.parse(result.stdout) : undefined;
}

async function check(name, run) {
  await run();
  checks++;
  console.log("PASS " + name);
}

function gatewayRequest(host, headers = {}) {
  return new Promise((resolve, reject) => {
    const req = request(
      gateway,
      { headers: { host, ...headers }, signal: AbortSignal.timeout(10000) },
      (res) => {
        res.resume();
        res.once("end", () =>
          resolve({ status: res.statusCode, version: res.headers["x-krun-version"] }),
        );
        res.once("error", reject);
      },
    );
    req.once("error", reject);
    req.end();
  });
}

function weights(...versions) {
  return versions.map(([version_id, percentage]) => ({ version_id, percentage }));
}

function status() {
  return cli("deployments", "status", ...scope, "--env", "production");
}

function build(label) {
  const source = path.join(dir, label + ".js");
  writeFileSync(
    source,
    `export default {
  fetch() {
    return new Response(${JSON.stringify(label)});
  },
};
`,
  );
  const queued = cli("build", "start", ...scope, "--env", "production", "--file", source);
  const ready = cli("build", "wait", ...scope, "--build", queued.id);
  assert.equal(ready.state, "succeeded");
  return ready.id;
}

try {
  execFileSync("go", ["build", "-trimpath", "-o", binary, "./cmd/krun"], { timeout: 120000 });
  await check("login from stdin and create an isolated Worker", () => {
    const login = command(["login", "--token-stdin"], token + "\n");
    assert.equal(login.status, 0, login.stderr);
    assert.match(login.stdout, /API Key saved/);
    assert(cli("whoami").user.user_id);
    worker = cli("workers", "create", "--org", org, "--name", "cli-rollout-" + run);
    scope = ["--org", org, "--worker", worker.id];
  });

  let stable;
  let candidate;
  let host;
  await check("build and deploy a stable version through the native CLI", async () => {
    const operation = cli("deploy", ...scope, "--env", "production", "--build", build("stable"));
    assert.equal(operation.state, "available");
    stable = operation.version_id;
    assert.deepEqual(status().versions, weights([stable, 100]));
    const env = cli("workers", "environments", "list", ...scope).items.find(
      (item) => item.name === "production",
    );
    host = env.hosts[0];
    assert(host);
    assert.deepEqual(await gatewayRequest(host), { status: 200, version: stable });
  });

  await check("upload, list and inspect a candidate without changing traffic", async () => {
    candidate = cli(
      "versions",
      "upload",
      ...scope,
      "--env",
      "production",
      "--build",
      build("candidate"),
      "--tag",
      "canary",
    ).id;
    assert.notEqual(candidate, stable);
    assert(
      cli("versions", "list", ...scope, "--env", "production").items.some(
        (version) => version.id === candidate,
      ),
    );
    assert.equal(cli("versions", "view", candidate, ...scope).tag, "canary");
    assert.deepEqual(status().versions, weights([stable, 100]));
    assert.equal((await gatewayRequest(host)).version, stable);
  });

  await check("stage a 0% candidate, wait by operation ID and verify via Gateway", async () => {
    const queued = cli(
      "versions",
      "deploy",
      stable,
      candidate + "@0%",
      ...scope,
      "--env",
      "production",
      "--yes",
      "--wait=false",
    );
    const ready = cli("deployments", "wait", ...scope, "--operation", queued.id);
    assert.equal(ready.state, "available");
    assert.deepEqual(status().versions, weights([stable, 100], [candidate, 0]));
    const selection = {
      "cloudflare-workers-version-overrides": worker.locator + '="' + candidate + '"',
    };
    assert.equal((await gatewayRequest(host, selection)).status, 403);
    const ordinary = await Promise.all(Array.from({ length: 8 }, () => gatewayRequest(host)));
    assert(ordinary.every((reply) => reply.status === 200 && reply.version === stable));
    const verified = await gatewayRequest(host, { ...selection, authorization: "Bearer " + token });
    assert.deepEqual(verified, { status: 200, version: candidate });
  });

  await check(
    "publish fractional percentages and read them back with the deployment message",
    () => {
      const message = "Start fractional rollout";
      const released = cli(
        "versions",
        "deploy",
        stable + "@94.875%",
        candidate + "@5.125",
        "-y",
        ...scope,
        "--env",
        "production",
        "--message",
        message,
      );
      assert.equal(released.state, "available");
      const expected = weights([stable, 94.875], [candidate, 5.125]);
      assert.deepEqual(status().versions, expected);
      const page = cli("deployments", "list", ...scope, "--env", "production", "--limit", "1");
      assert(page.next_after);
      const [entry] = page.items;
      assert.equal(entry.operation_id, released.id);
      assert.equal(entry.message, message);
      assert.deepEqual(entry.versions, expected);
      assert.deepEqual(cli("deployments", "view", entry.id, ...scope).versions, expected);
      assert.equal(
        cli("deployments", "list", ...scope, "--env", "production", "--after", page.next_after)
          .items[0].versions[1].percentage,
        0,
      );
    },
  );

  await check("reject an invalid total and a stale revision without changing the release", () => {
    const before = status();
    const invalid = command([
      "versions",
      "deploy",
      stable + "@90%",
      candidate + "@5%",
      ...scope,
      "--env",
      "production",
    ]);
    assert.notEqual(invalid.status, 0);
    assert.match(invalid.stderr, /traffic percentages must total 100%/);
    const stale = command([
      "versions",
      "deploy",
      stable,
      candidate,
      ...scope,
      "--env",
      "production",
      "--revision",
      String(before.deployment_revision - 1),
    ]);
    assert.notEqual(stale.status, 0);
    assert.match(stale.stderr, /REVISION_CONFLICT|Environment version changed/);
    assert.deepEqual(status(), before);
  });

  await check("distribute omitted percentages equally and keep per-session affinity", async () => {
    const result = cli("versions", "deploy", ...scope, stable, candidate, "--env", "production");
    assert.equal(result.state, "available");
    assert.deepEqual(status().versions, weights([stable, 50], [candidate, 50]));
    const seen = await Promise.all(
      Array.from({ length: 24 }, async (_, index) => {
        const key = "session-" + index;
        const [krun, cf] = await Promise.all([
          gatewayRequest(host, { "krun-workers-version-key": key }),
          gatewayRequest(host, { "cloudflare-workers-version-key": key }),
        ]);
        assert.equal(krun.status, 200);
        assert.deepEqual(cf, krun);
        return krun.version;
      }),
    );
    assert.deepEqual(new Set(seen), new Set([stable, candidate]));
  });

  await check("shift to 0/100, promote to a single version and roll back", async () => {
    cli("versions", "deploy", stable + "@0%", candidate + "@100%", ...scope, "--env", "production");
    assert.deepEqual(status().versions, weights([stable, 0], [candidate, 100]));
    assert.equal((await gatewayRequest(host)).version, candidate);
    cli("versions", "deploy", candidate, ...scope, "--env", "production");
    assert.deepEqual(status().versions, weights([candidate, 100]));
    const reverted = cli(
      "rollback",
      stable,
      ...scope,
      "--env",
      "production",
      "--yes",
      "--message",
      "Restore stable",
    );
    assert.equal(reverted.state, "available");
    assert.deepEqual(status().versions, weights([stable, 100]));
    assert.equal(
      cli("deployments", "list", ...scope, "--limit", "1").items[0].message,
      "Restore stable",
    );
    assert.equal((await gatewayRequest(host)).version, stable);
  });

  console.log("KRUN CLI E2E PASSED " + checks + " checks");
} finally {
  try {
    if (scope) cli("workers", "delete", "--org", org, "--id", worker.id);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}
