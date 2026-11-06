import { fetchConfiguration } from "./storage.js";
import {
  normalizePath,
  compileRules,
  assetDecision,
  applyHeaders,
  assetResponse,
} from "./assets.js";
import { WorkerEntrypoint } from "cloudflare:workers";
import { prepareStorageRequest, createCursor, createMultipartToken } from "./storage/signer.js";
import { cacheRequest } from "./cache.js";

// Only completed plain data is cached across workerd requests. Each signing call gets
// its own request context; no request-owned I/O object or promise is retained.
const storageGrants = new Map();
async function storageGrant(env, props) {
  if (env.VALIDATOR_ONLY === "true" || props.validation === true) {
    throw new Error("Storage access is unavailable during validation");
  }
  if (Date.now() - lastSync > 60000) {
    throw new Error("Storage authorization feed is stale");
  }
  const key = props.version_id + "\0" + props.binding + "\0" + props.bucket_id;
  const cached = storageGrants.get(key);
  if (
    cached &&
    cached.bucket_id === props.bucket_id &&
    Date.parse(cached.lease_until) > Date.now() + 5000 &&
    (!cached.expires_at || Date.parse(cached.expires_at) > Date.now() + 120000)
  ) {
    return cached;
  }
  storageGrants.delete(key);
  const grant = await platform(
    env,
    "/versions/" +
      encodeURIComponent(props.version_id) +
      "/bindings/" +
      encodeURIComponent(props.binding) +
      "/credentials",
    { method: "POST" },
  );
  if (
    grant.version_id !== props.version_id ||
    grant.binding !== props.binding ||
    grant.bucket_id !== props.bucket_id ||
    !grant.secret_access_key ||
    Date.parse(grant.lease_until) <= Date.now()
  ) {
    throw new Error("Storage grant mismatch");
  }
  lruPut(storageGrants, key, grant, 1024);
  return grant;
}
export class VersionStorage extends WorkerEntrypoint {
  async fetch(request) {
    if (request.method !== "POST" || !request.body) {
      return json({ error: "Invalid signing request" }, 400);
    }
    const reader = request.body.getReader();
    const chunks = [];
    let size = 0;
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) {
          break;
        }
        size += value.byteLength;
        if (size > 32768) {
          await reader.cancel();
          return json({ error: "Signing request is too large" }, 413);
        }
        chunks.push(value);
      }
      const bytes = new Uint8Array(size);
      let offset = 0;
      for (const chunk of chunks) {
        bytes.set(chunk, offset);
        offset += chunk.byteLength;
      }
      const args = JSON.parse(new TextDecoder().decode(bytes));
      const action = new URL(request.url).pathname;
      if (action === "/sign") {
        return json(await this.sign(args.operation, args.key, args.options));
      }
      if (action === "/cursor") {
        return json(await this.cursor(args.token, args.options));
      }
      if (action === "/multipart-token") {
        return json(
          await createMultipartToken(
            await storageGrant(this.env, this.ctx.props),
            args.key,
            args.uploadId,
            args.version,
          ),
        );
      }
      return json({ error: "Unknown signing operation" }, 400);
    } catch (error) {
      const invalid = error instanceof TypeError || error instanceof SyntaxError;
      return json(
        {
          type: invalid ? "TypeError" : "Error",
          error: invalid ? "Invalid storage signing options" : "Storage signer unavailable",
        },
        invalid ? 400 : 503,
      );
    } finally {
      reader.releaseLock();
    }
  }
  async sign(operation, key, options) {
    return prepareStorageRequest(
      await storageGrant(this.env, this.ctx.props),
      operation,
      key,
      options,
    );
  }
  async cursor(token, options) {
    return createCursor(await storageGrant(this.env, this.ctx.props), token, options);
  }
}
export class StorageDisabled extends WorkerEntrypoint {
  fetch() {
    throw new Error("Storage access is unavailable during validation");
  }
}

export class VersionCache extends WorkerEntrypoint {
  fetch(request) {
    const scope = this.ctx.props.namespace;
    if (typeof scope !== "string" || !scope || scope.length > 1024) {
      return new Response(null, { status: 400 });
    }
    return cacheRequest(scope, request);
  }
}

// workerd's HTTP disconnect does not reliably invoke a JS stream's cancel
// callback. The trusted entrypoint observes its incoming signal explicitly and
// cancels the RPC body reader, which aborts the child's storage request.
function storageResponse(request, response) {
  if (!response.body) {
    return new Response(null, response);
  }
  const reader = response.body.getReader();
  let stopped = false;
  const stop = () => {
    if (stopped) {
      return;
    }
    stopped = true;
    request.signal.removeEventListener("abort", stop);
    reader.cancel().catch(() => {});
  };
  request.signal.addEventListener("abort", stop, { once: true });
  if (request.signal.aborted) {
    stop();
  }
  const stream = new ReadableStream({
    async pull(controller) {
      try {
        const result = await reader.read();
        if (result.done) {
          request.signal.removeEventListener("abort", stop);
          controller.close();
        } else {
          controller.enqueue(result.value);
        }
      } catch (error) {
        controller.error(error);
      }
    },
    cancel() {
      stop();
    },
  });
  return new Response(stream, response);
}

// Edge configuration is an immutable in-memory view. There is no local DB:
// bootstrap reads one snapshot, then a long-poll applies incremental ops.
let view = { revision: 0, routes: new Map(), versions: new Map() };
let versionRefs = new Map();
let lazyRoutes = false;
const routeDescriptors = new WeakMap();
const routeVersions = new Map();
const routeFlights = new Map();
const routeMisses = new Map();
const ROUTE_CACHE_MAX = 4096;
let viewLoaded = false;
let booting = null;
let started = false;
let lastSync = 0;
let lastLoop = 0;
const encoder = new TextEncoder();
const decoder = new TextDecoder();
const strictDecoder = new TextDecoder("utf-8", { fatal: true });

const json = (value, status = 200) => Response.json(value, { status });

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const sha256 = async (raw) =>
  [
    ...new Uint8Array(
      await crypto.subtle.digest("SHA-256", typeof raw === "string" ? encoder.encode(raw) : raw),
    ),
  ]
    .map((x) => x.toString(16).padStart(2, "0"))
    .join("");

// The bundle profile is manifest.json + one or more modules/* entries
// (+ assets/ in schema 2). Return every regular file so the manifest verifier
// can reject undeclared payloads rather than silently ignoring them.
function untar(raw) {
  const files = new Map();
  let off = 0;

  while (off + 512 <= raw.length) {
    const header = raw.subarray(off, off + 512);
    const base = decoder.decode(header.subarray(0, 100)).replace(/\0.*$/, "");
    const prefix = decoder.decode(header.subarray(345, 500)).replace(/\0.*$/, "");
    const name = prefix ? prefix + "/" + base : base;
    const sizeField = decoder
      .decode(header.subarray(124, 136))
      .replace(/[\0 ].*$/, "")
      .trim();
    const size = parseInt(sizeField, 8) || 0;
    const type = header[156];

    if (!name) {
      break;
    }

    const end = off + 512 + size;
    const next = off + 512 + Math.ceil(size / 512) * 512;

    if (type !== 48 || size < 0 || end > raw.length || next > raw.length || files.has(name)) {
      throw new Error("invalid artifact entry");
    }

    if (name !== "manifest.json" && !name.startsWith("modules/") && !name.startsWith("assets/")) {
      throw new Error("unsupported artifact entry");
    }

    files.set(name, raw.subarray(off + 512, end));
    off = next;
  }

  return files;
}

const portableUSTAR = (name) => {
  const bytes = encoder.encode(name).length;

  if (bytes <= 100) {
    return true;
  }

  if (bytes > 256) {
    return false;
  }

  for (let index = name.lastIndexOf("/"); index >= 0; index = name.lastIndexOf("/", index - 1)) {
    if (
      encoder.encode(name.slice(0, index)).length <= 155 &&
      encoder.encode(name.slice(index + 1)).length <= 100
    ) {
      return true;
    }
  }

  return false;
};

const safeModuleName = (name, maxBytes = 240, prefix = "modules/") =>
  typeof name === "string" &&
  name !== "" &&
  encoder.encode(name).length <= maxBytes &&
  !name.startsWith("/") &&
  !name.startsWith("../") &&
  !name.includes("\\") &&
  !name.split("/").some((part) => part === "" || part === "." || part === "..") &&
  ![...name].some((char) => {
    const code = char.codePointAt(0);

    return code < 0x20 || code === 0x7f;
  }) &&
  portableUSTAR(prefix + name);

const sameStrings = (a, b) =>
  Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((v, i) => v === b[i]);

const exactKeys = (value, keys) =>
  value &&
  typeof value === "object" &&
  !Array.isArray(value) &&
  Object.keys(value).every((key) => keys.includes(key));

const bytesBuffer = (bytes) => bytes.slice().buffer;

async function loadArtifact(archive, descriptor) {
  const files = untar(await gunzip(archive));
  const manifestBytes = files.get("manifest.json");

  if (!manifestBytes) {
    throw new Error("artifact manifest missing");
  }

  let manifest;

  try {
    manifest = JSON.parse(strictDecoder.decode(manifestBytes));
  } catch {
    throw new Error("artifact manifest invalid");
  }

  if (
    !exactKeys(manifest, [
      "schema_version",
      "profile",
      "compatibility_date",
      "compatibility_flags",
      "main_module",
      "modules",
      "assets",
    ]) ||
    manifest.schema_version !== 2 ||
    manifest.profile !== "workerd-bundle-v2" ||
    typeof manifest.compatibility_date !== "string" ||
    !/^\d{4}-\d{2}-\d{2}$/.test(manifest.compatibility_date) ||
    manifest.compatibility_date < "2021-11-02" ||
    manifest.compatibility_date > "2026-09-28" ||
    (manifest.compatibility_flags !== undefined &&
      (!Array.isArray(manifest.compatibility_flags) ||
        manifest.compatibility_flags.length > 32 ||
        manifest.compatibility_flags.some(
          (flag) => typeof flag !== "string" || !/^[a-z][a-z0-9_]{0,63}$/.test(flag),
        ) ||
        new Set(manifest.compatibility_flags).size !== manifest.compatibility_flags.length)) ||
    !safeModuleName(manifest.main_module) ||
    !Array.isArray(manifest.modules) ||
    manifest.modules.length === 0 ||
    manifest.modules.length > 512
  ) {
    throw new Error("artifact manifest unsupported");
  }

  manifest.compatibility_flags = manifest.compatibility_flags || [];

  if (
    descriptor.profile !== manifest.profile ||
    descriptor.compatibility_date !== manifest.compatibility_date ||
    !sameStrings(descriptor.compatibility_flags || [], manifest.compatibility_flags)
  ) {
    throw new Error("artifact compatibility mismatch");
  }

  const allowedTypes = new Set(["esm", "commonjs", "wasm", "text", "data", "source-map"]);
  const modules = {};
  const declared = new Set(["manifest.json"]);
  let mainDigest = "";
  let total = 0;

  for (const module of manifest.modules) {
    if (
      !exactKeys(module, ["name", "path", "type", "sha256", "size_bytes"]) ||
      !safeModuleName(module.name) ||
      module.path !== "modules/" + module.name ||
      !allowedTypes.has(module.type) ||
      typeof module.sha256 !== "string" ||
      !/^[0-9a-f]{64}$/.test(module.sha256) ||
      !Number.isInteger(module.size_bytes) ||
      module.size_bytes < 0 ||
      module.size_bytes > 2 * 1024 * 1024 ||
      declared.has(module.path)
    ) {
      throw new Error("artifact module metadata invalid");
    }

    const bytes = files.get(module.path);

    if (!bytes || bytes.length !== module.size_bytes || (await sha256(bytes)) !== module.sha256) {
      throw new Error("artifact module mismatch");
    }

    declared.add(module.path);
    total += bytes.length;

    if (total > 8 * 1024 * 1024) {
      throw new Error("artifact modules too large");
    }

    if (module.name === manifest.main_module) {
      if (module.type !== "esm" && module.type !== "commonjs") {
        throw new Error("artifact main module type invalid");
      }

      mainDigest = module.sha256;
    }

    try {
      if (module.type === "esm") {
        modules[module.name] = { js: strictDecoder.decode(bytes) };
      } else if (module.type === "commonjs") {
        modules[module.name] = { cjs: strictDecoder.decode(bytes) };
      } else if (module.type === "text") {
        modules[module.name] = { text: strictDecoder.decode(bytes) };
      } else if (module.type === "data") {
        modules[module.name] = { data: bytesBuffer(bytes) };
      } else if (module.type === "wasm") {
        if (
          bytes.length < 8 ||
          bytes[0] !== 0 ||
          bytes[1] !== 97 ||
          bytes[2] !== 115 ||
          bytes[3] !== 109
        ) {
          throw new Error("artifact wasm module invalid");
        }

        modules[module.name] = { wasm: bytesBuffer(bytes) };
      } else if (module.type === "source-map") {
        // Source maps are integrity-checked archive metadata, not Loader modules.
        JSON.parse(strictDecoder.decode(bytes));
      }
    } catch {
      throw new Error("artifact text module invalid");
    }
  }

  if (!mainDigest || mainDigest !== descriptor.module_sha256) {
    throw new Error("artifact main module mismatch");
  }

  if (manifest.assets !== undefined) {
    const assets = manifest.assets;

    if (
      !exactKeys(assets, [
        "directory",
        "binding",
        "run_worker_first",
        "not_found_handling",
        "html_handling",
        "base_path",
        "_headers",
        "_redirects",
        "files",
      ]) ||
      !Array.isArray(assets.files)
    ) {
      throw new Error("artifact assets invalid");
    }

    for (const file of assets.files) {
      if (
        !exactKeys(file, ["path", "sha256", "size_bytes", "content_type"]) ||
        !safeModuleName(file.path, 240, "assets/") ||
        typeof file.sha256 !== "string" ||
        typeof file.content_type !== "string" ||
        !Number.isInteger(file.size_bytes)
      ) {
        throw new Error("artifact asset metadata invalid");
      }

      const archivePath = "assets/" + file.path;
      const bytes = files.get(archivePath);

      if (
        declared.has(archivePath) ||
        !bytes ||
        bytes.length !== file.size_bytes ||
        (await sha256(bytes)) !== file.sha256
      ) {
        throw new Error("artifact asset mismatch");
      }

      declared.add(archivePath);
    }
  }

  if (files.size !== declared.size || [...files.keys()].some((name) => !declared.has(name))) {
    throw new Error("artifact contains undeclared entries");
  }

  return { manifest, modules };
}

async function platformResponse(env, path, init = {}, timeout = 2000) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeout);
  const headers = new Headers(init.headers);
  headers.set("authorization", `Bearer ${env.RUNTIME_TOKEN}`);
  headers.set("x-runtime-pool", env.RUNTIME_POOL || "default");

  if (init.body) {
    headers.set("content-type", "application/json");
  }

  try {
    return await env.PLATFORM.fetch(
      new Request("http://platform/api/internal/workers" + path, {
        ...init,
        headers,
        signal: controller.signal,
      }),
    );
  } finally {
    clearTimeout(timer);
  }
}

async function platform(env, path, init = {}) {
  const response = await platformResponse(env, path, init);

  if (!response.ok) {
    throw new Error("platform " + response.status);
  }

  return response.json();
}

async function requestJSON(env, url, timeout) {
  const response = await fetchConfiguration(env, url, timeout);

  if (!response.ok) {
    throw new Error("config " + response.status);
  }

  return response.json();
}

async function requestBytes(env, url, timeout) {
  const response = await fetchConfiguration(env, url, timeout);

  if (!response.ok) {
    throw new Error("config " + response.status);
  }

  return new Uint8Array(await response.arrayBuffer());
}

async function gunzip(raw) {
  return new Uint8Array(
    await new Response(
      new Blob([raw]).stream().pipeThrough(new DecompressionStream("gzip")),
    ).arrayBuffer(),
  );
}

function applySnapshot(snapshot, pool = "default") {
  if (snapshot.pool && snapshot.pool !== pool) {
    throw new Error("snapshot pool mismatch");
  }

  const routes = new Map();

  for (const route of snapshot.routes || []) {
    if (route.host) {
      routes.set(String(route.host).toLowerCase(), route);
    }
  }

  const versions = new Map();

  for (const [id, version] of Object.entries(snapshot.versions || {})) {
    versions.set(id, version);
  }

  versionRefs = new Map();

  for (const route of routes.values()) {
    if (route.version) {
      versionRefs.set(route.version, (versionRefs.get(route.version) || 0) + 1);
    }
  }

  for (const route of routes.values()) {
    routeDescriptors.set(route, versions.get(route.version));
  }

  view = { revision: Number(snapshot.config_revision || 0), routes, versions };
  lazyRoutes = snapshot.lazy_routes === true;
  routeVersions.clear();
  routeMisses.clear();
  viewLoaded = true;
  lastSync = Date.now();
}

// Bootstrap prefers object storage so a new Pod starts even while the control
// plane is unavailable; the control plane is the fallback.
async function bootstrap(env) {
  if (env.CONFIG_BASE) {
    try {
      const latest = await requestJSON(env, env.CONFIG_BASE + "/latest.json", 5000);

      if (latest?.lazy_routes !== true) {
        throw new Error("unsupported config head");
      }

      applySnapshot({ ...latest, pool: env.RUNTIME_POOL || "default", routes: [], versions: {} });

      return view;
    } catch (e) {
      console.error("config bootstrap from storage failed", e.message);
    }
  }

  return bootstrapFromControl(env);
}

async function bootstrapFromControl(env) {
  const head = await platform(env, "/config/snapshot");

  if (head?.lazy_routes !== true) {
    throw new Error("unsupported config head");
  }

  applySnapshot(head, env.RUNTIME_POOL || "default");

  return view;
}

function ensureBootstrap(env) {
  if (viewLoaded) {
    return Promise.resolve(view);
  }

  if (!booting) {
    booting = bootstrap(env).finally(() => {
      booting = null;
    });
  }

  return booting;
}

function applyBatch(batch) {
  if (!viewLoaded || Number(batch.from) !== view.revision) {
    return false;
  }

  // Validate before mutating: a malformed batch must never leave a partial view.
  const ops = batch.ops || [];

  for (const op of ops) {
    if (op.type === "version_put" && op.version?.id) {
      continue;
    }

    if (op.type === "route_upsert" && op.host && op.route?.host === op.host) {
      continue;
    }

    if (op.type === "route_delete" && op.host) {
      continue;
    }

    throw new Error("invalid config op " + op.type);
  }

  // No await occurs in this commit. Requests capture their immutable descriptor
  // before yielding; updating the indexes cannot change an in-flight version.
  const prune = new Set();
  const needed = new Set();

  for (const op of ops) {
    if (
      op.type === "route_upsert" &&
      view.routes.has(String(op.host).toLowerCase()) &&
      op.route.version
    ) {
      needed.add(op.route.version);
    }
  }

  for (const op of ops) {
    if (
      op.type === "version_put" &&
      lazyRoutes &&
      !needed.has(op.version.id) &&
      !versionRefs.has(op.version.id)
    ) {
      continue;
    }

    if (op.type === "version_put") {
      view.versions.set(op.version.id, op.version);
      prune.add(op.version.id);
      continue;
    }

    const host = String(op.host).toLowerCase();
    const old = view.routes.get(host);
    routeMisses.delete(host);

    if (lazyRoutes && (!old || (routeVersions.get(host) || 0) > Number(batch.to))) {
      continue;
    }

    routeVersions.set(host, Number(batch.to));

    if (old?.version) {
      const n = (versionRefs.get(old.version) || 0) - 1;

      if (n > 0) {
        versionRefs.set(old.version, n);
      } else {
        versionRefs.delete(old.version);
      }

      prune.add(old.version);
    }

    if (op.type === "route_delete") {
      view.routes.delete(host);
      routeVersions.delete(host);
    } else {
      view.routes.set(host, op.route);
      routeDescriptors.set(op.route, view.versions.get(op.route.version));

      if (op.route.version) {
        versionRefs.set(op.route.version, (versionRefs.get(op.route.version) || 0) + 1);
      }
    }
  }

  for (const id of prune) {
    if (!versionRefs.has(id)) {
      view.versions.delete(id);
    }
  }

  view.revision = Number(batch.to);
  lastSync = Date.now();

  return true;
}

function forgetRoute(host) {
  const route = view.routes.get(host);
  view.routes.delete(host);
  routeVersions.delete(host);

  if (route?.version) {
    const n = (versionRefs.get(route.version) || 0) - 1;

    if (n > 0) {
      versionRefs.set(route.version, n);
    } else {
      versionRefs.delete(route.version);
      view.versions.delete(route.version);
    }
  }
}

async function resolveRoute(env, host) {
  const cached = view.routes.get(host);

  if (cached) {
    view.routes.delete(host);
    view.routes.set(host, cached);

    return cached;
  }

  if (!lazyRoutes) {
    return null;
  }

  if ((routeMisses.get(host) || 0) > Date.now()) {
    return null;
  }

  return coalesce(routeFlights, host, async () => {
    for (let attempt = 0; attempt < 3; attempt++) {
      const before = view.revision;
      let snapshot;

      try {
        const response = await platformResponse(
          env,
          "/config/routes/" + encodeURIComponent(host),
          {},
          2000,
        );

        if (response.status === 404) {
          lruPut(routeMisses, host, Date.now() + 2000, 1024);

          return null;
        }

        if (!response.ok) {
          throw new Error("route lookup " + response.status);
        }

        snapshot = await response.json();
      } catch (e) {
        if (!env.CONFIG_BASE) {
          throw e;
        }

        const shard = (await sha256(host)).slice(0, 2);
        let latest;

        try {
          latest = await requestJSON(
            env,
            env.CONFIG_BASE + "/shards/" + shard + "/latest.json",
            2000,
          );
        } catch {
          throw e;
        }

        const raw = await requestBytes(env, env.CONFIG_BASE + "/" + latest.snapshot, 5000);

        if ((await sha256(raw)) !== latest.sha256) {
          throw new Error("route shard digest mismatch");
        }

        snapshot = JSON.parse(decoder.decode(await gunzip(raw)));
      }

      if (snapshot.pool !== (env.RUNTIME_POOL || "default")) {
        throw new Error("route lookup pool mismatch");
      }

      if (before !== view.revision) {
        continue;
      }

      const route = (snapshot.routes || []).find((r) => r.host === host);

      if (!route) {
        lruPut(routeMisses, host, Date.now() + 2000, 1024);

        return null;
      }

      const version = (snapshot.versions || {})[route.version];

      if (route.version && !version) {
        throw new Error("route descriptor missing");
      }

      forgetRoute(host);
      view.routes.set(host, route);
      routeVersions.set(host, Number(snapshot.config_revision || 0));

      if (version) {
        view.versions.set(version.id, version);
        versionRefs.set(version.id, (versionRefs.get(version.id) || 0) + 1);
      }

      routeDescriptors.set(route, version);

      while (view.routes.size > ROUTE_CACHE_MAX) {
        forgetRoute(view.routes.keys().next().value);
      }

      return route;
    }

    throw new Error("route changed during cold lookup");
  });
}

async function sendAck(env) {
  try {
    await platform(env, "/ack", {
      method: "POST",
      body: JSON.stringify({
        pod: env.POD_NAME,
        pod_uid: env.POD_UID,
        pool: env.RUNTIME_POOL || "default",
        revision: view.revision,
      }),
    });
  } catch {}
}

// One long-poll per Pod. 5s waits keep ack heartbeats fresh without the old
// 500ms full-snapshot polling loop.
async function follow(env) {
  let backoff = 1000;
  let lastAck = 0;

  while (true) {
    lastLoop = Date.now();

    try {
      await ensureBootstrap(env);

      if (Date.now() - lastAck > 5000) {
        await sendAck(env);
        lastAck = Date.now();
      }

      const response = await platformResponse(
        env,
        `/config/changes?since=${view.revision}&wait=5s`,
        {},
        30000,
      );

      if (response.status === 204) {
        lastSync = Date.now();
        await sleep(250);
        backoff = 1000;
        continue;
      }

      if (response.status === 409) {
        viewLoaded = false;
        await bootstrapFromControl(env);
        lastAck = 0;
        backoff = 1000;
        continue;
      }

      if (!response.ok) {
        throw new Error("config changes " + response.status);
      }

      const batch = await response.json();
      let applied = false;

      try {
        applied = applyBatch(batch);
      } catch (e) {
        viewLoaded = false;
        throw e;
      }

      if (!applied) {
        viewLoaded = false;
        await bootstrapFromControl(env);
      }

      backoff = 1000;
    } catch (e) {
      await sleep(backoff);
      backoff = Math.min(backoff * 2, 15000);
    }
  }
}

function ensureStarted(env, ctx) {
  // A cancelled waitUntil callback may never run follow()'s finally; restart a
  // loop that has made no progress for two minutes.
  if (started && Date.now() - lastLoop < 120000) {
    return;
  }

  started = true;
  const loop = follow(env).finally(() => {
    started = false;
  });

  if (ctx && typeof ctx.waitUntil === "function") {
    ctx.waitUntil(loop);
  }
}

// Asset manifests are immutable per version; skip re-fetching them per request.
const manifests = new Map();
const manifestFlights = new Map();
const assetFlights = new Map();

function lruPut(cache, key, value, max) {
  cache.delete(key);
  cache.set(key, value);

  while (cache.size > max) {
    cache.delete(cache.keys().next().value);
  }
}

function coalesce(cache, key, load) {
  if (cache.has(key)) {
    return cache.get(key);
  }

  const flight = Promise.resolve()
    .then(load)
    .finally(() => cache.delete(key));
  cache.set(key, flight);

  return flight;
}

async function assetManifest(env, version) {
  const cached = manifests.get(version.id);

  if (cached) {
    lruPut(manifests, version.id, cached, 512);

    return cached;
  }

  return coalesce(manifestFlights, version.id, async () => {
    const m = await platform(env, "/versions/" + encodeURIComponent(version.id) + "/assets");
    // Build the path index once per immutable manifest, including ASSETS binding.
    m.index = new Map((m.files || []).map((file) => ["/" + file.path, file.path]));
    m.rules = compileRules(m.config || {});
    lruPut(manifests, version.id, m, 512);

    return m;
  });
}

// Per-file lazy loading from the shared bucket, with a bounded in-memory LRU.
// workerd has no filesystem, so this is the local cache.
const assetCache = new Map();
let assetCacheBytes = 0;

function cacheGet(key) {
  const hit = assetCache.get(key);

  if (hit) {
    assetCache.delete(key);
    assetCache.set(key, hit);
  }

  return hit;
}

function cachePut(env, key, value) {
  const previous = assetCache.get(key);

  if (previous) {
    assetCacheBytes -= previous.bytes.length;
  }

  assetCache.set(key, value);
  assetCacheBytes += value.bytes.length;
  const max = Number(env.ASSETS_CACHE_MAX || 0) || 64 * 1024 * 1024;

  while (assetCacheBytes > max && assetCache.size > 0) {
    const first = assetCache.keys().next().value;
    const old = assetCache.get(first);
    assetCacheBytes -= old.bytes.length;
    assetCache.delete(first);
  }
}

async function assetBytes(env, version, path) {
  const key = version.id + "\0" + path;
  const hit = cacheGet(key);

  if (hit) {
    return hit;
  }

  return coalesce(assetFlights, key, async () => {
    const meta = await platform(
      env,
      "/versions/" +
        encodeURIComponent(version.id) +
        "/assets/" +
        path.split("/").map(encodeURIComponent).join("/"),
    );
    const response = await fetch(meta.download_url, { signal: AbortSignal.timeout(15000) });

    if (!response.ok) {
      throw new Error("asset download failed (" + response.status + ")");
    }

    const bytes = new Uint8Array(await response.arrayBuffer());

    if (bytes.length !== meta.size_bytes || (await sha256(bytes)) !== meta.sha256) {
      throw new Error("asset mismatch");
    }

    const value = { bytes, content_type: meta.content_type, digest: meta.sha256 };
    cachePut(env, key, value);

    return value;
  });
}

async function staticAssetResponse(env, version, request, manifest, binding = false) {
  const hit = assetDecision(request, manifest.config, manifest.index, manifest.rules, binding);

  if (hit.redirect !== undefined) {
    return new Response(null, { status: hit.status || 307, headers: { location: hit.redirect } });
  }

  if (hit.file) {
    const value = await assetBytes(env, version, manifest.index.get(hit.file));

    return applyHeaders(
      assetResponse(request, hit.file, value, hit.status),
      request,
      manifest.rules,
    );
  }

  if (hit.status) {
    return new Response(null, { status: hit.status });
  }

  return null;
}

async function boundAssetResponse(env, version, request) {
  const manifest = await assetManifest(env, version);

  if (!manifest.config) {
    return json({ error: "assets unavailable" }, 404);
  }

  return (
    (await staticAssetResponse(env, version, request, manifest, true)) ||
    json({ error: "asset not found" }, 404)
  );
}

export class VersionAssets extends WorkerEntrypoint {
  async fetch(request) {
    const versionId = this.ctx?.props?.version_id;

    if (typeof versionId !== "string" || versionId === "") {
      return json({ error: "asset binding unavailable" }, 404);
    }

    return boundAssetResponse(this.env, { id: versionId }, request);
  }
}
const bindingName = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;
const reservedBindings = new Set([
  "PLATFORM",
  "LOADER",
  "RUNTIME_TOKEN",
  "RUNTIME_POOL",
  "MANAGEMENT_TOKEN",
  "CONFIG_BASE",
  "POD_NAME",
  "POD_UID",
  "ASSETS_CACHE_MAX",
  "STORAGE_ACCESS_KEY",
  "STORAGE_SECRET_KEY",
  "STORAGE_REGION",
  "STATIC_DOMAINS",
]);
const magicBindings = new Set(["__proto__", "prototype", "constructor"]);

async function worker(env, ctx, d, artifact, validation) {
  const validating = validation !== undefined;
  const mode = validating ? "validation" : "serving";
  return env.LOADER.get(
    `${d.organization_id}/${d.service_id}/${d.environment}/${d.id}/${mode}/workerd-20260928/workerd-bundle-v2`,
    async () => {
      // The platform hands out a short-lived URL; artifact bytes go straight from
      // object storage to this Pod, never through the control plane. Candidate
      // validation may supply the URL with the management request instead.
      const a =
        artifact || (await platform(env, "/artifacts/" + encodeURIComponent(d.artifact_id)));
      const response = await fetch(a.download_url, { signal: AbortSignal.timeout(15000) });

      if (!response.ok) {
        throw new Error("artifact download failed (" + response.status + ")");
      }

      const archive = new Uint8Array(await response.arrayBuffer());

      if (archive.length !== a.size || (await sha256(archive)) !== a.archive_sha256) {
        throw new Error("artifact archive mismatch");
      }

      const loaded = await loadArtifact(archive, d);
      const manifest = loaded.manifest;
      let secrets = {};

      if (d.secret_digest) {
        const payload = validating
          ? validation.secrets
          : await platform(env, "/versions/" + encodeURIComponent(d.id) + "/secrets");

        if (
          !payload ||
          payload.version_id !== d.id ||
          payload.secret_digest !== d.secret_digest ||
          !payload.values ||
          typeof payload.values !== "object"
        ) {
          throw new Error("secret binding mismatch");
        }

        secrets = payload.values;
      }

      // Object spread defines own data properties even for magic names such as
      // `__proto__`; the assets binding below uses defineProperty for the same reason.
      // A normal prototype is required because WorkerLoader structured-clones env.
      const childEnv = {
        ...(d.variables || {}),
        ...secrets,
        KRUN_ENVIRONMENT: d.environment,
        KRUN_SERVICE_ID: d.service_id,
        KRUN_VERSION_ID: d.id,
      };

      let mainModule = manifest.main_module;
      if (typeof env.CACHE_SOURCE === "string" && ctx?.exports?.VersionCache) {
        const reserved = [
          "__krun_cache_entry__.mjs",
          "__krun_cache__/cache.js",
          "__krun_cache__/prelude.js",
        ];
        if (reserved.some((name) => Object.hasOwn(loaded.modules, name))) {
          throw new Error("Reserved cache module name");
        }
        childEnv.KRUN_CACHE = ctx.exports.VersionCache({
          props: {
            namespace: JSON.stringify([
              d.organization_id,
              d.service_id,
              d.environment,
              validating ? "validation/" + d.id : "serving",
            ]),
          },
        });
        loaded.modules["__krun_cache__/cache.js"] = { js: env.CACHE_SOURCE };
        loaded.modules["__krun_cache__/prelude.js"] = {
          js: `import {env} from 'cloudflare:workers';
import {createCacheStorage} from './cache.js';
Object.defineProperty(globalThis,'caches',{value:createCacheStorage(env.KRUN_CACHE),configurable:false,writable:false});`,
        };
        loaded.modules["__krun_cache_entry__.mjs"] = {
          js: `import './__krun_cache__/prelude.js'; export {default} from ${JSON.stringify("./" + mainModule)}; export * from ${JSON.stringify("./" + mainModule)};`,
        };
        mainModule = "__krun_cache_entry__.mjs";
      }
      if (Object.keys(d.r2_buckets || {}).length) {
        const targets = JSON.parse(env.OBJECT_STORES || "{}");
        for (const [name, binding] of Object.entries(d.r2_buckets)) {
          if (
            !bindingName.test(name) ||
            name.startsWith("KRUN_") ||
            reservedBindings.has(name) ||
            magicBindings.has(name) ||
            Object.hasOwn(childEnv, name) ||
            !ctx?.exports?.VersionStorage
          ) {
            throw new Error("Invalid R2 binding");
          }
          const target = targets[binding.store_id];
          if (!validating && (!target || !env[target.binding])) {
            throw new Error("R2 storage transport is unavailable");
          }
          childEnv[name] = {
            signer: ctx.exports.VersionStorage({
              props: {
                version_id: d.id,
                binding: name,
                bucket_id: binding.bucket_id,
                validation: validating,
              },
            }),
            transport: validating
              ? ctx.exports.StorageDisabled({ props: {} })
              : env[target.binding],
          };
        }
        const reserved = [
          "__krun_r2_entry__.mjs",
          "__krun_r2__/binding.js",
          "__krun_r2__/metadata.js",
          "__krun_r2__/xml.js",
          "__krun_r2__/prelude.js",
        ];
        if (reserved.some((name) => Object.hasOwn(loaded.modules, name))) {
          throw new Error("Reserved storage module name");
        }
        loaded.modules["__krun_r2__/binding.js"] = { js: env.R2_BINDING_SOURCE };
        loaded.modules["__krun_r2__/metadata.js"] = { js: env.R2_METADATA_SOURCE };
        loaded.modules["__krun_r2__/xml.js"] = { js: env.R2_XML_SOURCE };
        loaded.modules["__krun_r2__/prelude.js"] = {
          js: `import {env} from 'cloudflare:workers';
import {createR2Bucket,createStorageSigner} from './binding.js';
for(const name of ${JSON.stringify(Object.keys(d.r2_buckets))}) {
 const capability=env[name];
 Object.defineProperty(env,name,{value:createR2Bucket(createStorageSigner(capability.signer),capability.transport,{legacyListInclude:${JSON.stringify(manifest.compatibility_date < "2022-08-04" && !manifest.compatibility_flags.includes("r2_list_honor_include"))}}),enumerable:true});
}`,
        };
        loaded.modules["__krun_r2_entry__.mjs"] = {
          js: `import './__krun_r2__/prelude.js'; export {default} from ${JSON.stringify("./" + mainModule)}; export * from ${JSON.stringify("./" + mainModule)};`,
        };
        mainModule = "__krun_r2_entry__.mjs";
      }

      if (d.version_metadata) {
        const name = d.version_metadata;

        if (
          !bindingName.test(name) ||
          name.startsWith("KRUN_") ||
          reservedBindings.has(name) ||
          magicBindings.has(name) ||
          Object.hasOwn(childEnv, name)
        ) {
          throw new Error("version metadata binding name conflicts with environment");
        }

        Object.defineProperty(childEnv, name, {
          value: { id: d.id, tag: d.tag || "", timestamp: d.created_at },
          enumerable: true,
          writable: false,
          configurable: false,
        });
      }

      const assetBinding = manifest.assets?.binding;

      if (assetBinding) {
        if (
          !bindingName.test(assetBinding) ||
          assetBinding.startsWith("KRUN_") ||
          reservedBindings.has(assetBinding) ||
          magicBindings.has(assetBinding) ||
          Object.hasOwn(childEnv, assetBinding)
        ) {
          throw new Error("asset binding name conflicts with environment");
        }

        if (!ctx?.exports?.VersionAssets) {
          throw new Error("asset binding factory unavailable");
        }

        Object.defineProperty(childEnv, assetBinding, {
          value: ctx.exports.VersionAssets({ props: { version_id: d.id } }),
          enumerable: true,
          writable: false,
          configurable: false,
        });
      }

      return {
        compatibilityDate: manifest.compatibility_date,
        compatibilityFlags: manifest.compatibility_flags,
        mainModule,
        modules: loaded.modules,
        env: childEnv,
        globalOutbound: null,
      };
    },
  );
}

async function validate(env, ctx, d, artifact, secrets) {
  const stub = await worker(env, ctx, d, artifact, { secrets });
  await stub.getEntrypoint("KrunValidation").ping();
}

async function staticDomainResponse(env, request, prefix) {
  const path = normalizePath(new URL(request.url).pathname);
  const parts = (path || "").slice(1).split("/");
  const [organization, service, digest] = parts;
  const name = parts.slice(3).join("/");

  if (
    !path ||
    parts.length < 4 ||
    !organization ||
    !service ||
    !/^[0-9a-f]{64}$/.test(digest) ||
    !safeModuleName(name, 240, "assets/")
  ) {
    return json({ error: "invalid static asset path" }, 403);
  }

  if (request.method !== "GET" && request.method !== "HEAD") {
    return new Response(null, { status: 405 });
  }

  const key = "static/" + prefix + path;
  let value = cacheGet(key);

  if (!value) {
    value = await coalesce(assetFlights, key, async () => {
      const current = cacheGet(key);

      if (current) {
        return current;
      }

      const metadata = await platformResponse(
        env,
        "/static-assets/" + prefix + "/" + parts.map(encodeURIComponent).join("/"),
      );

      if (metadata.status === 404) {
        return { missing: true };
      }

      if (!metadata.ok) {
        throw new Error("static asset metadata " + metadata.status);
      }

      const meta = await metadata.json();

      if (meta.path !== name || meta.size_bytes < 0 || meta.size_bytes > 5 * 1024 * 1024) {
        throw new Error("static asset metadata mismatch");
      }

      const response = await fetch(meta.download_url, { signal: AbortSignal.timeout(15000) });

      if (!response.ok) {
        throw new Error("static asset download " + response.status);
      }

      const bytes = new Uint8Array(await response.arrayBuffer());

      if (bytes.length !== meta.size_bytes || (await sha256(bytes)) !== meta.sha256) {
        throw new Error("static asset integrity mismatch");
      }

      const loaded = { bytes, digest: meta.sha256, content_type: meta.content_type };
      cachePut(env, key, loaded);

      return loaded;
    });
  }

  if (value.missing) {
    return json({ error: "static asset not found" }, 404);
  }

  return assetResponse(request, name, value);
}

export class Private extends WorkerEntrypoint {
  async fetch(request) {
    try {
      const url = new URL(request.url);
      const staticDomains = JSON.parse(this.env.STATIC_DOMAINS || "{}");

      if (Object.hasOwn(staticDomains, url.hostname)) {
        return await staticDomainResponse(this.env, request, staticDomains[url.hostname]);
      }

      ensureStarted(this.env, this.ctx);
      await ensureBootstrap(this.env);
      const route = await resolveRoute(this.env, url.hostname.toLowerCase());

      if (!route) {
        return json({ error: "unknown route" }, 404);
      }

      if (route.expires_at && Date.parse(route.expires_at) <= Date.now()) {
        return json({ error: "deployment expired" }, 410);
      }

      if (!route.enabled) {
        return json({ error: "environment disabled" }, 410);
      }

      const d = routeDescriptors.get(route) || view.versions.get(route.version);

      if (!d) {
        return json({ error: "environment not published" }, 503);
      }

      const headers = new Headers(request.headers);

      for (const name of ["x-krun-version", "x-krun-scope", "x-forwarded-host", "x-internal"]) {
        headers.delete(name);
      }

      const forward = async () => {
        const stub = await worker(this.env, this.ctx, d);

        return await stub
          .getEntrypoint()
          .fetch(new Request(request, { headers, signal: request.signal }));
      };
      let response;
      const manifest = await assetManifest(this.env, d);

      if (!manifest.config) {
        response = await forward();
      } else {
        response = (await staticAssetResponse(this.env, d, request, manifest)) || (await forward());
      }

      const out = Object.keys(d.r2_buckets || {}).length
        ? storageResponse(request, response)
        : new Response(response.body, response);
      out.headers.set("x-krun-version", d.id);
      out.headers.set("x-krun-pod", this.env.POD_UID);

      return out;
    } catch (e) {
      console.error("runtime request failed", e.message);

      return json({ error: "runtime unavailable" }, 503);
    }
  }
}
export class Management extends WorkerEntrypoint {
  async fetch(request) {
    // The readiness probe starts the follow loop, so an idle Pod still converges
    // and acks new config revisions even when it receives no business traffic.
    const validatorOnly = this.env.VALIDATOR_ONLY === "true";

    if (!validatorOnly) {
      ensureStarted(this.env, this.ctx);
    }

    const path = new URL(request.url).pathname;

    if (
      path !== "/ready" &&
      request.headers.get("authorization") !== `Bearer ${this.env.MANAGEMENT_TOKEN}`
    ) {
      return json({ error: "denied" }, 403);
    }

    try {
      if (path === "/validate") {
        const input = await request.json();

        if (!input.version || !input.artifact) {
          return json({ error: "version and artifact required" }, 400);
        }

        // The controller supplies the immutable descriptor and a short-lived
        // artifact URL; cold-load and run the KrunValidation RPC on this member.
        await validate(this.env, this.ctx, input.version, input.artifact, input.secrets);

        return json({
          pod_uid: this.env.POD_UID,
          version_id: input.version.id,
          archive_sha256: input.version.archive_sha256,
        });
      }

      if (path !== "/ready") {
        return json({ error: "not found" }, 404);
      }

      if (validatorOnly) {
        return json({
          status: "ready",
          role: "validator",
          validation_protocol: 1,
          pod_uid: this.env.POD_UID,
          pool: this.env.RUNTIME_POOL || "default",
        });
      }

      // Readiness means the config view is loaded; versions are lazy-loaded on
      // first request so Pod start does not scale with the number of services.
      await ensureBootstrap(this.env);

      return json({
        status: "ready",
        role: "runtime",
        validation_protocol: 1,
        pod_uid: this.env.POD_UID,
        pool: this.env.RUNTIME_POOL || "default",
        revision: view.revision,
        stale_ms: Date.now() - lastSync,
      });
    } catch (e) {
      const dependency = String(e.message).startsWith("platform");
      console.error("runtime management failed", e.message);

      return json(
        {
          code: dependency ? "DEPENDENCY_UNAVAILABLE" : "CANDIDATE_INVALID",
          error: "runtime validation failed",
        },
        503,
      );
    }
  }
}
export default {
  fetch() {
    return json({ error: "entrypoint required" }, 404);
  },
};
