import { fixtureConfigEpoch } from "./entry-source.mjs";
import { fixtureRead, readObjectURL } from "./read-grant.mjs";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { once } from "node:events";
import {
  existsSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createServer, request as httpRequest } from "node:http";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { gunzipSync, gzipSync } from "node:zlib";
const here = path.dirname(fileURLToPath(import.meta.url));
const runtimeRoot = path.resolve(here, "..");
const repositoryRoot = path.resolve(runtimeRoot, "..", "..");
const builderRoot = path.join(repositoryRoot, "workers", "builder");
const wrangler = path.join(builderRoot, "node_modules", ".bin", "wrangler");
const workerd = path.join(runtimeRoot, "node_modules", ".bin", "workerd");
if (!existsSync(wrangler)) {
  if (process.env.CI) {
    throw new Error(
      "Wrangler artifact smoke requires npm ci --prefix workers/builder before runtime tests",
    );
  }
  console.log("WRANGLER ARTIFACT WORKERD SMOKE SKIPPED (builder dependencies are not installed)");
  process.exit(0);
}
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const scratch = realpathSync(mkdtempSync(path.join(tmpdir(), "krun-wrangler-workerd-")));
const work = path.join(scratch, "work");
mkdirSync(work, { recursive: true });
function tarFile(name, bytes) {
  bytes = Buffer.from(bytes);
  let base = name;
  let prefix = "";
  if (Buffer.byteLength(base, "utf8") > 100) {
    const parts = name.split("/");
    while (parts.length > 1 && Buffer.byteLength(parts.join("/"), "utf8") > 100) {
      prefix += (prefix ? "/" : "") + parts.shift();
    }
    base = parts.join("/");
  }
  const header = Buffer.alloc(512);
  header.write(base, 0, 100, "utf8");
  if (prefix) {
    header.write(prefix, 345, 155, "utf8");
  }
  const octal = (offset, length, value) =>
    header.write(value.toString(8).padStart(length - 1, "0") + "\0", offset, length, "ascii");
  octal(100, 8, 0o644);
  octal(108, 8, 0);
  octal(116, 8, 0);
  octal(124, 12, bytes.length);
  octal(136, 12, 0);
  header.fill(32, 148, 156);
  header[156] = 48;
  header.write("ustar\0", 257, 6, "ascii");
  header.write("00", 263, 2, "ascii");
  const sum = header.reduce((value, byte) => value + byte, 0);
  header.write(sum.toString(8).padStart(6, "0") + "\0 ", 148, 8, "ascii");
  return Buffer.concat([header, bytes, Buffer.alloc((512 - (bytes.length % 512)) % 512)]);
}
function sourceArchive(files) {
  return gzipSync(
    Buffer.concat([
      ...Object.entries(files).map(([name, bytes]) => tarFile("src/" + name, bytes)),
      Buffer.alloc(1024),
    ]),
  );
}
function packArchive(entries) {
  return gzipSync(
    Buffer.concat([
      ...Array.from(entries, ([name, bytes]) => tarFile(name, bytes)),
      Buffer.alloc(1024),
    ]),
  );
}
function archiveEntries(compressed) {
  const raw = gunzipSync(compressed);
  const entries = new Map();
  for (let offset = 0; offset + 512 <= raw.length; ) {
    const header = raw.subarray(offset, offset + 512);
    const base = header.subarray(0, 100).toString("utf8").replace(/\0.*$/, "");
    const prefix = header.subarray(345, 500).toString("utf8").replace(/\0.*$/, "");
    if (!base) {
      break;
    }
    const name = prefix ? prefix + "/" + base : base;
    const size =
      Number.parseInt(
        header
          .subarray(124, 136)
          .toString("ascii")
          .replace(/[\0 ].*$/, "")
          .trim(),
        8,
      ) || 0;
    entries.set(name, Buffer.from(raw.subarray(offset + 512, offset + 512 + size)));
    offset += 512 + Math.ceil(size / 512) * 512;
  }
  return entries;
}
async function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const port = server.address().port;
      server.close((error) => (error ? reject(error) : resolve(port)));
    });
  });
}
async function runBuilder(inputPath) {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [path.join(builderRoot, "build-worker.mjs")], {
      cwd: builderRoot,
      env: {
        ...process.env,
        KRUN_BUILD_INPUT: inputPath,
        KRUN_BUILD_WORK: work,
        KRUN_BUILD_PHASE: "all",
        KRUN_WRANGLER_BIN: wrangler,
        KRUN_NPM_REGISTRY: "https://registry.npmjs.org/",
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let output = "";
    child.stdout.on("data", (chunk) => {
      output += chunk;
    });
    child.stderr.on("data", (chunk) => {
      output += chunk;
    });
    child.once("close", (code) => resolve({ code, output }));
  });
}
const project = sourceArchive({
  "wrangler.jsonc": Buffer.from(
    JSON.stringify({
      name: "wrangler-runtime-smoke",
      main: "src/index.mjs",
      compatibility_date: "2026-09-28",
      compatibility_flags: ["nodejs_compat"],
      rules: [
        { type: "Text", globs: ["**/*.txt"], fallthrough: true },
        { type: "Data", globs: ["**/*.bin"], fallthrough: true },
        { type: "CompiledWasm", globs: ["**/*.wasm"], fallthrough: true },
      ],
    }),
  ),
  "src/index.mjs": Buffer.from(`import { WorkerEntrypoint } from "cloudflare:workers";
import message from "./message.txt";
import payload from "./payload.bin";
import wasm from "./empty.wasm";
const values = () => ({
  message,
  data: [...new Uint8Array(payload)],
  wasm: wasm instanceof WebAssembly.Module,
});
export class KrunValidation extends WorkerEntrypoint {
  async ping() {
    const value = values();
    if (value.message !== "wrangler-to-workerd" || value.data.join(",") !== "4,5,6" || !value.wasm)
      throw new Error("typed modules missing");
    return true;
  }
}
async function waitForRelease(name) {
  for (let attempt = 0; attempt < 200; attempt++) {
    if (await caches.default.match("https://fixture.test/" + name)) return;
    await scheduler.wait(100);
  }
  throw new Error("test did not release pending work");
}
export class Named extends WorkerEntrypoint {
  fetch(request) {
    const suffix = new URL(request.url).search;
    this.ctx.waitUntil(waitForRelease("release" + suffix).then(() =>
      caches.default.put("https://fixture.test/drained" + suffix, new Response("finished")),
    ));
    return new Response("scheduled");
  }
}
export default {
  async fetch(request, env, ctx) {
    const { pathname: path, search } = new URL(request.url);
    if (path === "/release" || path === "/release-stream") {
      await caches.default.put("https://fixture.test" + path + search, new Response("release"));
      return new Response(null, { status: 204 });
    }
    if (path === "/background") {
      return (ctx.exports.test ?? ctx.exports.Named).fetch(request);
    }
    if (path === "/drain") {
      return new Response(new ReadableStream({
        async start(controller) {
          const encoder = new TextEncoder();
          controller.enqueue(encoder.encode("start "));
          await waitForRelease("release-stream");
          controller.enqueue(encoder.encode("finish"));
          controller.close();
        },
      }));
    }
    if (path === "/socket") {
      const [client, server] = Object.values(new WebSocketPair());
      server.accept();
      server.addEventListener("message", (event) => server.send(event.data));
      server.addEventListener("close", () => server.close());
      return new Response(null, { status: 101, webSocket: client });
    }
    if (path === "/drained") {
      return (await caches.default.match("https://fixture.test/drained" + search)) || new Response(null, { status: 404 });
    }
    return Response.json({ source: "real-wrangler-artifact", ...values() });
  },
};
`),
  "src/message.txt": Buffer.from("wrangler-to-workerd"),
  "src/payload.bin": Buffer.from([4, 5, 6]),
  "src/empty.wasm": Buffer.from([0, 97, 115, 109, 1, 0, 0, 0]),
});
let artifact = null;
let version = null;
let descriptor = null;
let artifactDownloads = 0;
const configRevision = 1;
let activeVersion;
let untracedVersion;
let untracedArtifact;
let untracedDescriptor;
const untracedHost = "untraced.wrangler.test";
let experimentalVersion;
let experimentalArtifact;
let experimentalDescriptor;
const experimentalHost = "experimental.wrangler.test";
const activeHost = "warm.wrangler.test";
const fixtureHost = "127.0.0.1";
const route = () => ({
  host: fixtureHost,
  path: "/",
  enabled: true,
  version: version.id,
});
async function eventually(check, message) {
  const deadline = Date.now() + 12000;
  while (Date.now() < deadline) {
    if (await check()) return;
    await delay(50);
  }
  assert.fail(message());
}
const server = createServer((request, response) => {
  const chunks = [];
  request.on("data", (chunk) => chunks.push(chunk));
  request.on("end", () => {
    if (request.url === "/source") {
      response.writeHead(200, { "content-type": "application/gzip" });
      response.end(project);
      return;
    }
    if (request.url === "/upload" && request.method === "PUT") {
      artifact = Buffer.concat(chunks);
      response.writeHead(200);
      response.end();
      return;
    }
    const objectPath = readObjectURL(
      new URL(request.url, "http://" + request.headers.host),
      request.method,
    ).pathname;
    if (objectPath === "/artifact") {
      artifactDownloads++;
      response.writeHead(200, {
        "content-type": "application/gzip",
        "content-length": artifact.length,
      });
      response.end(artifact);
      return;
    }
    if (objectPath === "/untraced-artifact") {
      artifactDownloads++;
      response.end(untracedArtifact);
      return;
    }
    if (objectPath === "/experimental-artifact") {
      response.end(experimentalArtifact);
      return;
    }
    if (request.url === "/api/internal/workers/config/snapshot") {
      response.setHeader("content-type", "application/json");
      response.end(
        JSON.stringify({
          pool: "default",
          config_epoch: fixtureConfigEpoch,
          config_revision: configRevision,
          lazy_routes: true,
          routes: [],
          versions: {},
        }),
      );
      return;
    }
    const alternate = new Map([
      [activeHost, activeVersion],
      [untracedHost, untracedVersion],
      [experimentalHost, experimentalVersion],
    ]);
    const host = request.url.split("/").at(-1);
    if (request.url.startsWith("/api/internal/workers/config/routes/") && alternate.has(host)) {
      const routedVersion = alternate.get(host);
      response.setHeader("content-type", "application/json");
      response.end(
        JSON.stringify({
          pool: "default",
          config_epoch: fixtureConfigEpoch,
          config_revision: configRevision,
          routes: [{ host, enabled: true, version: routedVersion.id }],
          versions: { [routedVersion.id]: routedVersion },
        }),
      );
      return;
    }
    if (request.url === "/api/internal/workers/config/routes/" + fixtureHost) {
      response.setHeader("content-type", "application/json");
      response.end(
        JSON.stringify({
          pool: "default",
          config_epoch: fixtureConfigEpoch,
          config_revision: configRevision,
          routes: [route()],
          versions: { [version.id]: version },
        }),
      );
      return;
    }
    if (request.url.startsWith("/api/internal/workers/config/changes")) {
      setTimeout(() => {
        response.writeHead(204, { "x-krun-config-epoch": fixtureConfigEpoch });
        response.end();
      }, 50);
      return;
    }
    if (request.url === "/api/internal/workers/ack") {
      response.setHeader("content-type", "application/json");
      response.end('{"ok":true}');
      return;
    }
    if (request.url === "/api/internal/workers/artifacts/" + version.artifact_id) {
      response.setHeader("content-type", "application/json");
      response.end(JSON.stringify(descriptor));
      return;
    }
    if (request.url === "/api/internal/workers/artifacts/" + untracedVersion?.artifact_id) {
      response.setHeader("content-type", "application/json");
      response.end(JSON.stringify(untracedDescriptor));
      return;
    }
    if (request.url === "/api/internal/workers/artifacts/" + experimentalVersion?.artifact_id) {
      response.setHeader("content-type", "application/json");
      response.end(JSON.stringify(experimentalDescriptor));
      return;
    }
    const assetsVersion = [version, activeVersion, untracedVersion, experimentalVersion].find(
      (candidate) => request.url === "/api/internal/workers/versions/" + candidate?.id + "/assets",
    );
    if (assetsVersion) {
      response.setHeader("content-type", "application/json");
      response.end(JSON.stringify({ version_id: assetsVersion.id, config: null, files: [] }));
      return;
    }
    response.writeHead(404);
    response.end("not found");
  });
});
await new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(0, "127.0.0.1", resolve);
});
const platformPort = server.address().port;
let child;
let exited = false;
let output = "";
try {
  const inputPath = path.join(scratch, "input.json");
  writeFileSync(
    inputPath,
    JSON.stringify({
      build_mode: "wrangler",
      config_path: "wrangler.jsonc",
      source_url: `http://127.0.0.1:${platformPort}/source`,
      upload_url: `http://127.0.0.1:${platformPort}/upload`,
      compatibility_date: "2026-09-28",
      compatibility_flags: ["nodejs_compat"],
    }),
  );
  const built = await runBuilder(inputPath);
  assert.equal(built.code, 0, built.output);
  assert.ok(artifact?.length, built.output);
  const entries = archiveEntries(artifact);
  const manifest = JSON.parse(entries.get("manifest.json").toString("utf8"));
  const main = manifest.modules.find((module) => module.name === manifest.main_module);
  assert.ok(main, "Wrangler artifact has no main module");
  assert.ok(
    manifest.modules.some((module) => module.type === "text"),
    "Wrangler artifact lost Text module",
  );
  assert.ok(
    manifest.modules.some((module) => module.type === "data"),
    "Wrangler artifact lost Data module",
  );
  assert.ok(
    manifest.modules.some((module) => module.type === "wasm"),
    "Wrangler artifact lost Wasm module",
  );
  version = {
    id: "rel_wrangler_runtime",
    worker_id: "svc",
    organization_id: "org",
    environment: "production",
    artifact_id: "art_wrangler_runtime",
    profile: manifest.profile,
    compatibility_date: manifest.compatibility_date,
    compatibility_flags: manifest.compatibility_flags,
    module_sha256: main.sha256,
    archive_sha256: sha256(artifact),
    variables: {},
    pool: "default",
  };
  assert.deepEqual(version.compatibility_flags, ["nodejs_compat"]);
  activeVersion = { ...version, id: "rel_wrangler_active" };
  const untracedMain = {
    name: "untraced.mjs",
    path: "modules/untraced.mjs",
    type: "esm",
  };
  const modulePath = JSON.stringify("./" + manifest.main_module);
  const untracedSource = Buffer.from(`
export { default, KrunValidation, Named as test } from ${modulePath};
`);
  untracedMain.sha256 = sha256(untracedSource);
  untracedMain.size_bytes = untracedSource.length;
  const untracedEntries = new Map(entries);
  untracedEntries.set(untracedMain.path, untracedSource);
  untracedEntries.set(
    "manifest.json",
    Buffer.from(
      JSON.stringify({
        ...manifest,
        main_module: untracedMain.name,
        modules: [...manifest.modules, untracedMain],
      }),
    ),
  );
  untracedArtifact = packArchive(untracedEntries);
  untracedVersion = {
    ...version,
    id: "rel_wrangler_untraced",
    artifact_id: "art_wrangler_untraced",
    module_sha256: untracedMain.sha256,
    archive_sha256: sha256(untracedArtifact),
  };
  untracedDescriptor = {
    artifact_id: untracedVersion.artifact_id,
    archive_sha256: untracedVersion.archive_sha256,
    size: untracedArtifact.length,
    ...fixtureRead(`http://127.0.0.1:${platformPort}/untraced-artifact`),
  };
  const experimentalFlags = ["experimental"];
  const experimentalEntries = new Map(entries);
  experimentalEntries.set(
    "manifest.json",
    Buffer.from(JSON.stringify({ ...manifest, compatibility_flags: experimentalFlags })),
  );
  experimentalArtifact = packArchive(experimentalEntries);
  experimentalVersion = {
    ...version,
    id: "rel_wrangler_experimental",
    artifact_id: "art_wrangler_experimental",
    archive_sha256: sha256(experimentalArtifact),
    compatibility_flags: experimentalFlags,
  };
  experimentalDescriptor = {
    artifact_id: experimentalVersion.artifact_id,
    archive_sha256: experimentalVersion.archive_sha256,
    size: experimentalArtifact.length,
    ...fixtureRead(`http://127.0.0.1:${platformPort}/experimental-artifact`),
  };
  descriptor = {
    artifact_id: version.artifact_id,
    archive_sha256: version.archive_sha256,
    size: artifact.length,
    ...fixtureRead(`http://127.0.0.1:${platformPort}/artifact`),
  };
  const privatePort = await freePort();
  const managementPort = await freePort();
  const entryPath = path.join(scratch, "entry.js");
  const entrySource = readFileSync(path.join(runtimeRoot, "entry.js"), "utf8");
  // Advance the entry Worker's clock without changing workerd's native timers.
  writeFileSync(
    entryPath,
    `
let testNow;
const Date = class extends globalThis.Date {
  static now() {
    return testNow ?? globalThis.Date.now();
  }
};
${entrySource}
export class SmokeManagement extends Management {
  fetch(request) {
    const now = request.headers.get("x-test-now");
    if (now !== null) {
      testNow = Number(now);
    }
    return super.fetch(request);
  }
}
`,
  );
  const config = `using W = import "/workerd/workerd.capnp";
const config :W.Config = (
 services = [
  (name="entry",worker=(modules=[(name="entry.js",esModule=embed ${JSON.stringify(entryPath)}),(name="assets.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot, "assets.js"))}),(name="service.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot, "service.js"))}),(name="cache.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot, "cache.js"))}),(name="storage.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot, "storage.js"))}),(name="storage/binding.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot, "storage/binding.js"))}),(name="storage/metadata.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot, "storage/metadata.js"))}),(name="storage/xml.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot, "storage/xml.js"))}),(name="storage/signature.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot, "storage/signature.js"))}),(name="storage/signer.js",esModule=embed ${JSON.stringify(path.join(runtimeRoot, "storage/signer.js"))})],compatibilityDate="2026-09-28",compatibilityFlags=["experimental","enable_ctx_exports","enable_request_signal","request_signal_passthrough"],bindings=[
   (name="CACHE_SOURCE",text=embed ${JSON.stringify(path.join(runtimeRoot, "cache.js"))}),(name="LOADER",workerLoader=(id="krun-wrangler-artifact-smoke")),
   (name="PLATFORM",service="platform"),(name="POD_NAME",text="pod"),(name="POD_UID",text="uid"),(name="RUNTIME_TOKEN",text="runtime-token"),(name="MANAGEMENT_TOKEN",text="management-token"),(name="RUNTIME_POOL",text="default"),(name="ASSETS_CACHE_MAX",text="1048576")
  ],globalOutbound="storage")),
  (name="platform",external=(address="127.0.0.1:${platformPort}",http=())),
  (name="storage",network=(allow=["127.0.0.1/32"]))
 ],
 sockets=[(name="private",address="127.0.0.1:${privatePort}",http=(),service=(name="entry",entrypoint="Private")),(name="management",address="127.0.0.1:${managementPort}",http=(),service=(name="entry",entrypoint="SmokeManagement"))]
);`;
  const configPath = path.join(scratch, "runtime.capnp");
  writeFileSync(configPath, config);
  child = spawn(workerd, ["serve", configPath, "-I", "/", "--experimental"], {
    cwd: scratch,
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout.on("data", (chunk) => {
    output += chunk;
  });
  child.stderr.on("data", (chunk) => {
    output += chunk;
  });
  child.once("exit", () => {
    exited = true;
  });
  let validation;
  for (let attempt = 0; attempt < 100; attempt++) {
    if (exited) {
      throw new Error("workerd exited before readiness\n" + output);
    }
    try {
      validation = await fetch(`http://127.0.0.1:${managementPort}/validate`, {
        method: "POST",
        headers: { authorization: "Bearer management-token", "content-type": "application/json" },
        body: JSON.stringify({ version, artifact: descriptor }),
      });
      break;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }
  assert.ok(validation, "workerd did not become ready\n" + output);
  const validationText = await validation.text();
  assert.equal(validation.status, 200, validationText + "\n" + output);
  function requestWorker(path, { firstChunk, host = fixtureHost } = {}) {
    return new Promise((resolve, reject) => {
      const request = httpRequest(
        {
          hostname: "127.0.0.1",
          port: privatePort,
          path,
          headers: { host },
        },
        (incoming) => {
          const chunks = [];
          incoming.once("data", () => firstChunk?.());
          incoming.on("data", (chunk) => chunks.push(chunk));
          incoming.on("error", reject);
          incoming.on("end", () =>
            resolve({
              status: incoming.statusCode,
              headers: incoming.headers,
              body: Buffer.concat(chunks).toString("utf8"),
            }),
          );
        },
      );
      request.on("error", reject);
      request.end();
    });
  }
  const idleMs = 60 * 60 * 1000;
  // Keep the simulated clock within the validity of the fixture's storage grants.
  const startedAt = Date.now() - 3 * idleMs - 120_000;
  const ready = (now) =>
    fetch(`http://127.0.0.1:${managementPort}/ready`, {
      headers: { "x-test-now": String(now) },
    }).then((r) => r.json());
  await ready(startedAt);
  const response = await requestWorker("/run");
  assert.equal(response.status, 200, response.body + "\n" + output);
  assert.deepEqual(JSON.parse(response.body), {
    source: "real-wrangler-artifact",
    message: "wrangler-to-workerd",
    data: [4, 5, 6],
    wasm: true,
  });
  assert.equal(response.headers["x-krun-version"], version.id);
  assert.ok(artifactDownloads >= 1, "runtime never downloaded the Wrangler artifact");
  assert.equal((await requestWorker("/run", { host: activeHost })).status, 200);
  assert.equal((await requestWorker("/background", { host: untracedHost })).body, "scheduled");
  const beforeRetirement = artifactDownloads;
  let streamStarted;
  const started = new Promise((resolve) => {
    streamStarted = resolve;
  });
  const stream = requestWorker("/drain", { firstChunk: streamStarted });
  await started;

  await ready(startedAt + idleMs - 5_000);
  await delay(100);
  const warm = await requestWorker("/run", { host: activeHost });
  assert.equal(warm.status, 200);
  assert.equal(warm.headers["x-krun-version"], activeVersion.id);
  assert.equal(
    artifactDownloads,
    beforeRetirement,
    "a Worker idle for less than an hour stays warm",
  );

  await ready(startedAt + idleMs);
  await delay(100);
  assert.ok(
    !output.includes("krun isolate retired"),
    "retirement interrupted a streaming response",
  );
  assert.equal((await requestWorker("/release-stream", { host: activeHost })).status, 204);
  assert.equal((await stream).body, "start finish");
  assert.equal((await requestWorker("/release", { host: activeHost })).status, 204);
  await eventually(
    async () => (await requestWorker("/drained", { host: activeHost })).body === "finished",
    () => "retirement interrupted waitUntil in the untraced test entrypoint",
  );

  const socket = new WebSocket(`ws://127.0.0.1:${privatePort}/socket`);
  await once(socket, "open", { signal: AbortSignal.timeout(3000) });
  const socketIdleAt = startedAt + 2 * idleMs;
  await ready(socketIdleAt - 5_000);
  assert.equal((await requestWorker("/run", { host: activeHost })).status, 200);
  await ready(socketIdleAt);
  await delay(100);
  const message = once(socket, "message", { signal: AbortSignal.timeout(3000) });
  socket.send("still connected");
  assert.equal((await message)[0].data, "still connected");
  const closed = once(socket, "close", { signal: AbortSignal.timeout(3000) });
  socket.close(1000);
  await closed;
  assert.equal((await requestWorker("/run", { host: activeHost })).status, 200);
  assert.equal((await requestWorker("/background?tracked")).body, "scheduled");
  const retireAt = startedAt + 3 * idleMs;
  await ready(retireAt - 5_000);
  assert.equal((await requestWorker("/run", { host: activeHost })).status, 200);
  await ready(retireAt);
  await delay(100);
  assert.ok(
    !output.includes("krun isolate retired"),
    "retirement interrupted a loopback waitUntil",
  );
  assert.equal((await requestWorker("/release?tracked", { host: activeHost })).status, 204);
  await eventually(
    async () => (await requestWorker("/drained?tracked", { host: activeHost })).body === "finished",
    () => "detached waitUntil never completed",
  );
  let sweepAt = retireAt + 5_000;
  await eventually(
    async () => {
      await ready(Math.min(sweepAt, Date.now()));
      sweepAt += 5_000;
      return output.includes("krun isolate retired");
    },
    () => "Worker with an enabled route was not retired after an hour without access\n" + output,
  );
  assert.equal(artifactDownloads, beforeRetirement, "retirement must not download user code");
  assert.equal((await requestWorker("/run", { host: untracedHost })).status, 200);
  assert.equal(artifactDownloads, beforeRetirement, "a Worker with untraced exports stays warm");
  assert.equal((await requestWorker("/run", { host: activeHost })).status, 200);
  assert.equal(artifactDownloads, beforeRetirement, "recently accessed Worker stays warm");
  const reloaded = await requestWorker("/run");
  assert.equal(reloaded.status, 200, reloaded.body + "\n" + output);
  assert.equal(artifactDownloads, beforeRetirement + 1, "next request must cold-load retired code");
  assert.equal((await requestWorker("/drained?tracked")).body, "finished");
  assert.equal(
    (await requestWorker("/run", { host: experimentalHost })).status,
    503,
    "streaming activity tracking must not grant experimental flags to user Workers",
  );
  assert.match(output, /compatibility flag experimental is experimental/);
  console.log("REAL WRANGLER ARTIFACT TO WORKERD SMOKE PASSED");
} finally {
  if (child && !exited) {
    child.kill("SIGTERM");
    await new Promise((resolve) => {
      const timeout = setTimeout(resolve, 2000);
      child.once("exit", () => {
        clearTimeout(timeout);
        resolve();
      });
    });
  }
  await new Promise((resolve) => server.close(resolve));
  rmSync(scratch, { recursive: true, force: true });
}
