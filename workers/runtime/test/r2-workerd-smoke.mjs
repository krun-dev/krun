import assert from "node:assert/strict";
import { createHash, createHmac } from "node:crypto";
import { readFileSync, writeFileSync, appendFileSync, mkdtempSync, rmSync } from "node:fs";
import http from "node:http";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { gzipSync } from "node:zlib";
import { fileURLToPath } from "node:url";
import { runProject } from "./r2-project-scenarios.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const liveFile = process.argv
  .find((value) => value.startsWith("--grant-file="))
  ?.slice("--grant-file=".length);
const liveGrant = liveFile ? JSON.parse(readFileSync(liveFile, "utf8")) : null;
const projectFile = process.argv
  .find((value) => value.startsWith("--project-file="))
  ?.slice("--project-file=".length);
const project = projectFile ? JSON.parse(readFileSync(projectFile, "utf8")) : null;
const directory = mkdtempSync(path.join(tmpdir(), "krun-r2-smoke-"));
const sha = (value) => createHash("sha256").update(value).digest("hex");
const hmac = (key, value) => createHmac("sha256", key).update(value).digest();
const encode = (value) =>
  encodeURIComponent(value).replace(
    /[!'()*]/g,
    (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase(),
  );
const listen = (server) =>
  new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));
const freePort = async () => {
  const server = net.createServer();
  const port = await listen(server);
  await new Promise((resolve) => server.close(resolve));
  return port;
};
function tar(files) {
  const chunks = [];
  for (const [name, value] of Object.entries(files)) {
    const bytes = Buffer.from(value);
    const header = Buffer.alloc(512);
    header.write(name, 0, 100);
    header.write("0000644\0", 100, 8);
    header.write("0000000\0", 108, 8);
    header.write("0000000\0", 116, 8);
    header.write(bytes.length.toString(8).padStart(11, "0") + "\0", 124, 12);
    header.fill(32, 148, 156);
    header[156] = 48;
    header.write("ustar\0", 257, 6);
    const checksum = header.reduce((a, b) => a + b, 0);
    header.write(checksum.toString(8).padStart(6, "0") + "\0 ", 148, 8);
    chunks.push(header, bytes, Buffer.alloc((512 - (bytes.length % 512)) % 512));
  }
  return gzipSync(Buffer.concat([...chunks, Buffer.alloc(1024)]));
}
const source = project
  ? readFileSync(project.source, "utf8")
  : `import { env as imported, WorkerEntrypoint } from 'cloudflare:workers';
const atModuleScope = imported.BUCKET;
export default class extends WorkerEntrypoint {
  async fetch(request) {
    if (atModuleScope !== this.env.BUCKET || typeof atModuleScope.resumeMultipartUpload !== 'function') {
      throw new Error('binding initialization mismatch');
    }
    if (new URL(request.url).pathname === '/download') {
      const object = await this.env.BUCKET.get('large.bin');
      return new Response(object.body);
    }
    if (new URL(request.url).pathname === '/direct-fixture') {
      const object = await this.env.BUCKET.get('console.txt');
      try {
        return Response.json({
          text: await object.text(),
          custom: object.customMetadata,
          type: object.httpMetadata.contentType,
        });
      } finally {
        await this.env.BUCKET.delete('console.txt');
      }
    }
    if (new URL(request.url).pathname === '/cache-fixture') {
      const key = 'https://cached.fixture.test/item';
      if (request.method === 'POST') {
        await caches.default.put(key, new Response('0123456789', {
          headers: {'Cache-Control':'max-age=60','Content-Length':'10','ETag':'"cache-v1"'},
        }));
        return new Response(null, {status:204});
      }
      if (request.method === 'DELETE') {
        return Response.json({deleted:await caches.default.delete(key)});
      }
      return await caches.default.match(new Request(key, {headers:request.headers})) || new Response(null, {status:404});
    }
    if (new URL(request.url).pathname === '/multipart-abort') {
      const url = new URL(request.url);
      const key = 'explorer/abort-fixture';
      if (url.searchParams.get('action') === 'create') {
        const upload = await this.env.BUCKET.createMultipartUpload(key);
        return Response.json({key,uploadId:upload.uploadId});
      }
      const upload = this.env.BUCKET.resumeMultipartUpload(key,url.searchParams.get('uploadId'));
      if (url.searchParams.get('action') === 'abort') {
        await upload.abort();
        return new Response(null,{status:204});
      }
      try {
        return Response.json(await upload.uploadPart(1,'discard'));
      } catch {
        return new Response(null,{status:400});
      }
    }
    try {
      const put = await this.env.BUCKET.put('hello.txt', request.body, {
        customMetadata: { '中文': '保留大小写' },
        httpMetadata: { contentType: 'text/plain' },
      });
      const get = await this.env.BUCKET.get('hello.txt');
      const text = await get.text();
      const conditional = await this.env.BUCKET.get('hello.txt', {
        onlyIf: new Headers({ 'if-none-match': get.httpEtag }),
      });
      const createConflict = await this.env.BUCKET.put('hello.txt', 'must-not-overwrite', {
        onlyIf: { etagDoesNotMatch: '*' },
      });
      const staleWrite = await this.env.BUCKET.put('hello.txt', 'must-not-overwrite', {
        onlyIf: { etagMatches: '00000000000000000000000000000000' },
      });
      const range = await this.env.BUCKET.get('hello.txt', { range: { offset: 0, length: 8 } });
      const rangeText = await range.text();
      const gzipBytes = new Uint8Array([31, 139, 8, 0, 0, 0, 0, 0, 2, 255, 75, 175, 202, 44, 80, 72, 203, 172, 40, 41, 45, 74, 5, 0, 54, 88, 60, 135, 12, 0, 0, 0]);
      await this.env.BUCKET.put('encoded.txt', gzipBytes, {httpMetadata:{contentEncoding:'gzip'}});
      const compressed = await this.env.BUCKET.get('encoded.txt');
      const compressedBytes = new Uint8Array(await compressed.arrayBuffer());
      if (compressedBytes.length !== gzipBytes.length || !compressedBytes.every((value,index)=>value===gzipBytes[index])) {
        throw new Error('compressed object bytes changed during transport');
      }
      await this.env.BUCKET.delete('encoded.txt');
      const list = await this.env.BUCKET.list({ include: ['customMetadata'] });
      for (const key of ['page-a','page-b','page-c']) {
        await this.env.BUCKET.put(key, '');
      }
      const firstPage = await this.env.BUCKET.list({prefix:'page-',limit:1});
      const pageKeys = firstPage.objects.map(object=>object.key);
      let cursor = firstPage.cursor;
      while (cursor) {
        const page = await this.env.BUCKET.list({prefix:'page-',limit:1,cursor});
        pageKeys.push(...page.objects.map(object=>object.key));
        cursor = page.cursor;
      }
      let tamperRejected = false;
      try {
        await this.env.BUCKET.list({prefix:'other',limit:1,cursor:firstPage.cursor});
      } catch {tamperRejected = true;}
      await this.env.BUCKET.delete(['page-a','page-b','page-c']);

      await this.env.BUCKET.delete('hello.txt');
      return Response.json({
        size: put.size,
        versionMatches: put.version === get.version,
        bodyUsed: get.bodyUsed,
        textLength: text.length,
        custom: get.customMetadata,
        listed: list.objects.map(o => o.key),
        conditionalBodyAbsent: !('body' in conditional),
        createConflict: createConflict === null,
        staleWriteConflict: staleWrite === null,
        range: rangeText,
        pageKeys,
        tamperRejected,
        missing: await this.env.BUCKET.head('hello.txt'),
      });
    } finally {
      await this.env.BUCKET.delete(['hello.txt','encoded.txt','page-a','page-b','page-c']);
    }
  }
}
export class KrunValidation extends WorkerEntrypoint {
  ping() { return { ok: true }; }
}
`;

const manifest = {
  schema_version: 2,
  profile: "workerd-bundle-v2",
  compatibility_date: project?.compatibility_date ?? "2026-09-28",
  compatibility_flags: [],
  main_module: "index.js",
  modules: [
    {
      name: "index.js",
      path: "modules/index.js",
      type: "esm",
      sha256: sha(source),
      size_bytes: Buffer.byteLength(source),
    },
  ],
};
const archive = tar({ "manifest.json": JSON.stringify(manifest), "modules/index.js": source });
const descriptor = {
  id: "version-r2",
  organization_id: "org-test",
  service_id: "svc-test",
  environment: "production",
  artifact_id: "artifact-r2",
  profile: manifest.profile,
  compatibility_date: manifest.compatibility_date,
  compatibility_flags: [],
  module_sha256: sha(source),
  variables: project?.variables ?? {},
  r2_buckets: {
    [project?.binding ?? "BUCKET"]: {
      bucket_id: "bucket-test",
      bucket_name: "test",
      store_id: "primary",
    },
  },
};
let grantRequests = 0;
let firstStorageData;
const storageStarted = new Promise((resolve) => {
  firstStorageData = resolve;
});
const objects = new Map();
let invalidSignature = false;
let largeFinished = false;
let largeCancelled;
const largeClosed = new Promise((resolve) => {
  largeCancelled = resolve;
});
function verify(request) {
  const url = new URL(request.url, "http://" + request.headers.host);
  const signature = url.searchParams.get("X-Amz-Signature");
  const credential = url.searchParams.get("X-Amz-Credential");
  const stamp = url.searchParams.get("X-Amz-Date");
  const names = url.searchParams.get("X-Amz-SignedHeaders");
  if (
    !signature ||
    !credential ||
    !stamp ||
    !names ||
    url.searchParams.get("X-Amz-Security-Token") !== "test-session" ||
    url.searchParams.get("X-Amz-Expires") !== "60"
  ) {
    return false;
  }
  url.searchParams.delete("X-Amz-Signature");
  const query = [...url.searchParams]
    .map(([k, v]) => [encode(k), encode(v)])
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([k, v]) => k + "=" + v)
    .join("&");
  const scope = credential.split("/").slice(1).join("/");
  const canonical = [
    request.method,
    url.pathname,
    query,
    names
      .split(";")
      .map((name) => name + ":" + String(request.headers[name]).trim().replace(/\s+/g, " ") + "\n")
      .join(""),
    names,
    "UNSIGNED-PAYLOAD",
  ].join("\n");
  let key = "AWS4test-secret";
  for (const component of scope.split("/")) {
    key = hmac(key, component);
  }
  return (
    hmac(key, "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + sha(canonical)).toString(
      "hex",
    ) === signature
  );
}
const storage = http.createServer(async (request, response) => {
  if (!verify(request)) {
    invalidSignature = true;
    response.writeHead(403);
    response.end();
    return;
  }
  const url = new URL(request.url, "http://storage.test");
  const key = decodeURIComponent(url.pathname);
  if (request.method === "PUT") {
    const current = objects.get(key);
    if (
      (request.headers["if-none-match"] === "*" && current) ||
      (request.headers["if-match"] && request.headers["if-match"] !== current?.etag)
    ) {
      request.resume();
      response.writeHead(412);
      response.end();
      return;
    }
    const chunks = [];
    for await (const chunk of request) {
      firstStorageData();
      chunks.push(chunk);
    }
    const bytes = Buffer.concat(chunks);
    objects.set(key, {
      bytes,
      metadata: request.headers["x-amz-meta-krun-v1"],
      etag: '"' + createHash("md5").update(bytes).digest("hex") + '"',
    });
    response.writeHead(200);
    response.end();
    return;
  }
  if (request.method === "DELETE") {
    objects.delete(key);
    response.writeHead(204);
    response.end();
    return;
  }
  if (url.searchParams.has("list-type")) {
    const prefix = "/test-bucket/" + url.searchParams.get("prefix");
    const all = [...objects.keys()].filter((key) => key.startsWith(prefix)).sort();
    const offset = Number(url.searchParams.get("continuation-token") ?? "0");
    const limit = Number(url.searchParams.get("max-keys"));
    const batch = all.slice(offset, offset + limit);
    const contents = batch
      .map((key) => "<Contents><Key>" + key.slice("/test-bucket/".length) + "</Key></Contents>")
      .join("");
    const truncated = offset + limit < all.length;
    response.writeHead(200, { "content-type": "application/xml" });
    response.end(
      "<ListBucketResult>" +
        contents +
        "<IsTruncated>" +
        truncated +
        "</IsTruncated>" +
        (truncated
          ? "<NextContinuationToken>" + (offset + limit) + "</NextContinuationToken>"
          : "") +
        "</ListBucketResult>",
    );
    return;
  }
  const object = objects.get(key);
  if (!object) {
    response.writeHead(404);
    response.end();
    return;
  }
  if (request.headers["if-none-match"] === object.etag) {
    response.writeHead(304);
    response.end();
    return;
  }
  if (key.endsWith("/large.bin")) {
    response.once("finish", () => {
      largeFinished = true;
    });
    response.once("close", () => {
      largeCancelled();
    });
  }
  let bytes = object.bytes;
  const headers = {
    "content-length": bytes.length,
    "last-modified": new Date().toUTCString(),
    etag: object.etag,
    "x-amz-meta-krun-v1": object.metadata,
  };
  if (request.headers.range) {
    const match = request.headers.range.match(/^bytes=(\d+)-(\d*)$/);
    const offset = Number(match[1]);
    const end = match[2] ? Number(match[2]) : bytes.length - 1;
    bytes = bytes.subarray(offset, end + 1);
    headers["content-length"] = bytes.length;
    headers["content-range"] = "bytes " + offset + "-" + end + "/" + object.bytes.length;
    response.writeHead(206, headers);
  } else {
    response.writeHead(200, headers);
  }
  if (key.endsWith("/large.bin") && request.method === "GET") {
    let offset = 0;
    let timer;
    const pump = () => {
      if (response.destroyed) {
        return;
      }
      if (offset >= bytes.length) {
        response.end();
        return;
      }
      const chunk = bytes.subarray(offset, offset + 64 * 1024);
      offset += chunk.length;
      if (response.write(chunk)) {
        timer = setTimeout(pump, 5);
      } else {
        response.once("drain", () => {
          timer = setTimeout(pump, 5);
        });
      }
    };
    response.once("close", () => clearTimeout(timer));
    pump();
    return;
  }
  response.end(request.method === "HEAD" ? undefined : bytes);
});
const storagePort = await listen(storage);
const mockGrant = {
  version_id: descriptor.id,
  binding: "BUCKET",
  bucket_id: "bucket-test",
  store_id: "primary",
  endpoint: "http://127.0.0.1:" + storagePort,
  region: "us-east-1",
  bucket: "test-bucket",
  prefix: "worker-data/org/test/buckets/test/",
  addressing_style: "path",
  access_key_id: "test-access",
  secret_access_key: "test-secret",
  session_token: "test-session",
  lease_until: new Date(Date.now() + 300000),
  expires_at: new Date(Date.now() + 3600000),
  cursor_key: "test-cursor-key",
};
const grant = liveGrant
  ? {
      ...liveGrant,
      version_id: descriptor.id,
      binding: project?.binding ?? "BUCKET",
      bucket_id: "bucket-test",
      store_id: "primary",
    }
  : mockGrant;
const platform = http.createServer((request, response) => {
  response.setHeader("content-type", "application/json");
  const url = new URL(request.url, "http://platform");
  if (url.pathname === "/archive") {
    response.end(archive);
    return;
  }
  if (url.pathname.endsWith("/config/snapshot")) {
    response.end(JSON.stringify({ pool: "default", config_revision: 1, lazy_routes: true }));
    return;
  }
  if (url.pathname.includes("/config/routes/")) {
    response.end(
      JSON.stringify({
        pool: "default",
        config_revision: 1,
        routes: [{ host: "fixture.test", enabled: true, version: descriptor.id }],
        versions: { [descriptor.id]: descriptor },
      }),
    );
    return;
  }
  if (url.pathname.endsWith("/config/changes")) {
    setTimeout(() => {
      response.writeHead(204);
      response.end();
    }, 200);
    return;
  }
  if (url.pathname.endsWith("/ack")) {
    response.end("{}");
    return;
  }
  if (url.pathname.endsWith("/assets")) {
    response.end(JSON.stringify({ config: null, files: [] }));
    return;
  }
  if (url.pathname.endsWith("/credentials")) {
    grantRequests++;
    response.end(JSON.stringify(grant));
    return;
  }
  if (url.pathname.includes("/artifacts/")) {
    response.end(
      JSON.stringify({
        download_url: "http://127.0.0.1:" + platform.address().port + "/archive",
        size: archive.length,
        archive_sha256: sha(archive),
      }),
    );
    return;
  }
  response.writeHead(404);
  response.end("{}");
});
const platformPort = await listen(platform);
const port = await freePort();
const moduleNames = [
  "entry.js",
  "assets.js",
  "cache.js",
  "storage.js",
  ...["binding", "metadata", "xml", "signature", "signer"].map((name) => "storage/" + name + ".js"),
];
const modules = moduleNames
  .map(
    (name) =>
      `(name=${JSON.stringify(name)},esModule=embed ${JSON.stringify(path.join(root, name))})`,
  )
  .join(",");
const sources = ["binding", "metadata", "xml"]
  .map(
    (name) =>
      `(name="R2_${name.toUpperCase()}_SOURCE",text=embed ${JSON.stringify(path.join(root, "storage", name + ".js"))})`,
  )
  .join(",");
const target = new URL(grant.endpoint);
if (grant.addressing_style === "virtual") {
  target.hostname = grant.bucket + "." + target.hostname;
}
const objectOptions =
  '(injectRequestHeaders=[(name="Host",value=' + JSON.stringify(target.host) + ")])";
const address =
  target.hostname + ":" + (target.port || (target.protocol === "https:" ? "443" : "80"));
const protocol =
  target.protocol === "https:"
    ? "https=(options=" +
      objectOptions +
      ",certificateHost=" +
      JSON.stringify(target.hostname) +
      ",tlsOptions=(trustBrowserCas=true))"
    : "http=" + objectOptions;
const objectService =
  '(name="objects",external=(address=' + JSON.stringify(address) + "," + protocol + "))";
const config = `using W=import "/workerd/workerd.capnp";
const config :W.Config=(services=[
 (name="entry",worker=(modules=[${modules}],compatibilityDate="2026-09-28",compatibilityFlags=["experimental","enable_ctx_exports","enable_request_signal","request_signal_passthrough"],bindings=[${sources},(name="CACHE_SOURCE",text=embed ${JSON.stringify(path.join(root, "cache.js"))}),
 (name="LOADER",workerLoader=(id="r2-smoke")),(name="PLATFORM",service="platform"),(name="POD_UID",text="uid"),(name="POD_NAME",text="pod"),(name="RUNTIME_POOL",text="default"),(name="RUNTIME_TOKEN",text="test"),
 (name="OBJECT_STORES",text=${JSON.stringify(JSON.stringify({ primary: { binding: "OBJECT_STORE_0" } }))}),(name="OBJECT_STORE_0",service="objects")],globalOutbound="network")),
 (name="platform",external=(address="127.0.0.1:${platformPort}",http=())),
 ${objectService},
 (name="network",network=(allow=["127.0.0.1/32"]))],
 sockets=[(name="private",address="127.0.0.1:${port}",http=(),service=(name="entry",entrypoint="Private"))]);`;
// Use JSON escaping for the nested text binding instead of Cap'n Proto escapes.
const configPath = path.join(directory, "runtime.capnp");
writeFileSync(configPath, config);
const child = spawn(
  path.join(root, "node_modules/.bin/workerd"),
  ["serve", configPath, "-I", "/", "--experimental"],
  { stdio: ["ignore", "pipe", "pipe"] },
);
let output = "";
child.stdout.on("data", (chunk) => (output += chunk));
child.stderr.on("data", (chunk) => (output += chunk));
const terminate = () => {
  child.kill("SIGTERM");
  process.exit(143);
};
process.once("SIGTERM", terminate);
const requestWorker = async (pathname, options = {}) => {
  // Undici may replace Host with the URL host. Use a real HTTP request so
  // the same private ingress and route resolution as the runtime smoke run.
  const input = new Request("http://fixture.test" + pathname, options);
  const bytes = input.body ? Buffer.from(await input.arrayBuffer()) : null;
  return new Promise((resolve, reject) => {
    const headers = Object.fromEntries(input.headers);
    headers.host = "fixture.test";
    if (bytes) {
      headers["content-length"] = bytes.length;
    }
    const outgoing = http.request(
      {
        hostname: "127.0.0.1",
        port,
        method: input.method,
        path: pathname,
        headers,
      },
      (response) => {
        const chunks = [];
        response.on("data", (chunk) => chunks.push(chunk));
        response.on("error", reject);
        response.on("end", () =>
          resolve(
            new Response(
              [204, 304].includes(response.statusCode) || input.method === "HEAD"
                ? null
                : Buffer.concat(chunks),
              { status: response.statusCode, headers: response.headers },
            ),
          ),
        );
      },
    );
    outgoing.on("error", reject);
    outgoing.setTimeout(15000, () =>
      outgoing.destroy(new Error("upstream HTTP scenario timed out")),
    );
    outgoing.end(bytes);
  });
};
try {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (child.exitCode !== null) {
      throw new Error("workerd exited: " + output);
    }
    try {
      const response = await fetch("http://127.0.0.1:" + port + "/", {
        headers: { host: "unknown.test" },
      });
      await response.body?.cancel();
      break;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 30));
    }
  }
  if (project) {
    await runProject(project, requestWorker);
    console.log(project.name + " upstream HTTP scenarios completed");
  } else {
    if (process.argv.includes("--direct-fixture")) {
      const response = await requestWorker("/direct-fixture");
      assert.equal(response.status, 200, "console upload was not readable by the R2 binding");
      assert.deepEqual(await response.json(), {
        text: "console direct upload: 中文",
        custom: { source: "console" },
        type: "text/plain",
      });
    }
    const bytes = 256 * 1024;
    const result = new Promise((resolve, reject) => {
      const request = http.request(
        {
          hostname: "127.0.0.1",
          port,
          method: "POST",
          path: "/",
          headers: { host: "fixture.test", "content-length": bytes },
        },
        (response) => {
          const chunks = [];
          response.on("data", (chunk) => chunks.push(chunk));
          response.on("end", () =>
            resolve({ status: response.statusCode, text: Buffer.concat(chunks).toString() }),
          );
        },
      );
      request.on("error", reject);
      if (liveGrant) {
        firstStorageData();
      }
      request.write(Buffer.alloc(64 * 1024, 65));
      Promise.race([
        storageStarted,
        new Promise((_, reject) =>
          setTimeout(
            () => reject(new Error("upload was buffered or never reached storage: " + output)),
            5000,
          ),
        ),
      ]).then(
        () => request.end(Buffer.alloc(bytes - 64 * 1024, 65)),
        (error) => {
          request.destroy();
          reject(error);
        },
      );
    });
    const response = await result;
    assert.equal(response.status, 200, response.text + "\n" + output);
    const body = JSON.parse(response.text);
    assert.deepEqual(body, {
      size: bytes,
      versionMatches: true,
      bodyUsed: true,
      textLength: bytes,
      custom: { 中文: "保留大小写" },
      listed: ["hello.txt"],
      conditionalBodyAbsent: true,
      createConflict: true,
      staleWriteConflict: true,
      range: "AAAAAAAA",
      pageKeys: ["page-a", "page-b", "page-c"],
      tamperRejected: true,
      missing: null,
    });
    if (!liveGrant) {
      assert.equal(invalidSignature, false, "signature or fixed Host mismatch");
    }
    assert.equal(grantRequests, 1, "completed grant was not cached across signer RPC calls");
    assert.equal(objects.size, 0, "fixture was not deleted");
    assert.equal((await requestWorker("/cache-fixture", { method: "POST" })).status, 204);
    assert.equal(await (await requestWorker("/cache-fixture")).text(), "0123456789");
    const cachedRange = await requestWorker("/cache-fixture", { headers: { range: "bytes=3-5" } });
    assert.equal(cachedRange.status, 206);
    assert.equal(await cachedRange.text(), "345");
    assert.equal(
      (await requestWorker("/cache-fixture", { headers: { "if-none-match": '"cache-v1"' } }))
        .status,
      304,
    );
    assert.equal(
      (await (await requestWorker("/cache-fixture", { method: "DELETE" })).json()).deleted,
      true,
    );
    assert.equal((await requestWorker("/cache-fixture")).status, 404);
    if (liveGrant) {
      const created = await requestWorker("/multipart-abort?action=create");
      assert.equal(created.status, 200);
      const upload = await created.json();
      appendFileSync(
        path.join(path.dirname(liveFile), "multipart.jsonl"),
        JSON.stringify({ key: upload.key, token: upload.uploadId }) + "\n",
        { mode: 0o600 },
      );
      const query = "&uploadId=" + encodeURIComponent(upload.uploadId);
      assert.equal((await requestWorker("/multipart-abort?action=part" + query)).status, 200);
      assert.equal((await requestWorker("/multipart-abort?action=abort" + query)).status, 204);
      assert.equal((await requestWorker("/multipart-abort?action=abort" + query)).status, 204);
      assert.equal((await requestWorker("/multipart-abort?action=part" + query)).status, 400);
    }
    if (!liveGrant) {
      const largeKey = "/test-bucket/" + grant.prefix + "large.bin";
      objects.set(largeKey, {
        bytes: Buffer.alloc(32 * 1024 * 1024, 66),
        etag: '"large"',
        metadata: Buffer.from(
          JSON.stringify({ version: "stream-test", customMetadata: {}, httpMetadata: {} }),
        ).toString("base64"),
      });
      await new Promise((resolve, reject) => {
        const request = http.get(
          { hostname: "127.0.0.1", port, path: "/download", headers: { host: "fixture.test" } },
          (response) => {
            assert.equal(response.statusCode, 200);
            response.once("data", () => {
              response.pause();
              setTimeout(() => {
                try {
                  assert.equal(largeFinished, false, "download was drained despite backpressure");
                  response.socket.resetAndDestroy();
                  resolve();
                } catch (error) {
                  reject(error);
                }
              }, 200);
            });
          },
        );
        request.on("error", reject);
      });
      await Promise.race([
        largeClosed,
        new Promise((_, reject) =>
          setTimeout(() => reject(new Error("download cancellation did not reach storage")), 3000),
        ),
      ]);
      assert.equal(largeFinished, false, "cancelled download was fully drained " + output);
      objects.delete(largeKey);
    }

    console.log(
      "R2 native WorkerLoader, module env, WorkerEntrypoint env, signed fixed Host, direct streaming and credential caching passed",
    );
  }
} catch (error) {
  if (project) {
    let safe = output;
    for (const field of ["access_key_id", "secret_access_key", "session_token", "cursor_key"]) {
      if (grant[field]) {
        safe = safe.replaceAll(grant[field], "[redacted]");
      }
    }
    console.error(safe);
  }
  throw error;
} finally {
  process.removeListener("SIGTERM", terminate);
  child.kill("SIGTERM");
  await Promise.race([
    new Promise((resolve) => child.once("exit", resolve)),
    new Promise((resolve) =>
      setTimeout(() => {
        child.kill("SIGKILL");
        resolve();
      }, 2000),
    ),
  ]);
  platform.closeAllConnections();
  storage.closeAllConnections();
  platform.close();
  storage.close();
  rmSync(directory, { recursive: true, force: true });
}
