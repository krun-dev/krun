// Executes the real workers/runtime/entry.js decision flow in Node with stubbed
// bindings (PLATFORM, LOADER, fetch). Not a substitute for kind e2e, but it runs
// the actual shipped code rather than a re-implementation.
import {readFileSync, writeFileSync, mkdtempSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {gzipSync} from 'node:zlib';
import assert from 'node:assert/strict';

const here = dirname(fileURLToPath(import.meta.url));
const source = readFileSync(join(here, '..', 'entry.js'), 'utf8');
const patched = source.replace(
  "import {WorkerEntrypoint} from 'cloudflare:workers';",
  'class WorkerEntrypoint { constructor(env, ctx) { this.env = env; this.ctx = ctx; } }'
);
const tmp = join(mkdtempSync(join(tmpdir(), 'krun-entry-')), 'entry.mjs');
writeFileSync(tmp, patched);
// workerd gives each isolate a single env, so entry.js caches the snapshot at
// module scope. Each scenario needs a fresh module instance to model that.
let instance = 0;
const freshEntry = () => import(tmp + '?v=' + Date.now() + '-' + (instance++));
const entry = await freshEntry();

const encoder = new TextEncoder();
async function digest(bytes) {
  const raw = typeof bytes === 'string' ? encoder.encode(bytes) : bytes;
  return [...new Uint8Array(await crypto.subtle.digest('SHA-256', raw))].map(x => x.toString(16).padStart(2, '0')).join('');
}

const release = {id: 'rel_test', service_id: 'svc', organization_id: 'org', environment: 'production', artifact_id: 'art', archive_sha256: 'a'.repeat(64), module_sha256: 'b'.repeat(64), variables: {}, pool: 'default'};
const files = {
  'index.html': '<!doctype html><title>root</title>',
  'about.html': '<!doctype html><h1>about</h1>',
  'docs.html': '<!doctype html><h1>docs</h1>',
  'assets/app-0123abcd.js': 'console.log("hashed")',
};
const meta = {};
for (const [name, text] of Object.entries(files)) {
  meta[name] = {sha256: await digest(text), size: text.length, type: name.endsWith('.js') ? 'application/javascript; charset=utf-8' : 'text/html; charset=utf-8'};
}
const config = {directory: 'public', run_worker_first: ['/api/*', '!/api/docs/*'], not_found_handling: 'single-page-application', html_handling: 'auto-trailing-slash'};

const downloads = [];
const configObjects = new Map();
globalThis.fetch = async (url) => {
  const raw = String(url);
  if (raw.startsWith('http://config.test/')) {
    const key = raw.replace('http://config.test/', '');
    downloads.push('config:' + key);
    if (!configObjects.has(key)) return new Response('missing', {status: 404});
    return new Response(configObjects.get(key), {status: 200});
  }
  const key = raw.replace('http://assets.test/', '');
  downloads.push(key);
  if (!(key in files)) return new Response('missing', {status: 404});
  return new Response(files[key], {status: 200});
};

function platformFor(manifestConfig, fileList, rel, options = {}) {
  const changes = options.changes || [];
  return {
    async fetch(request) {
      const url = new URL(request.url);
      const path = url.pathname.replace('/api/internal/workers', '');
      if (path === '/config/snapshot') {
        return Response.json({pool: 'default', config_revision: options.revision || 1, routes: [{host: 'w-test.workers.test', path: '/', enabled: true, release: rel.id}], releases: {[rel.id]: rel}});
      }
      if (path.startsWith('/config/changes')) {
        if (changes.length) return Response.json(changes.shift());
        return new Response(null, {status: 204});
      }
      if (path === '/ack') return Response.json({ok: true});
      const assetMatch = path.match(/^\/releases\/([^/]+)\/assets$/);
      if (assetMatch) {
        const id = decodeURIComponent(assetMatch[1]);
        return Response.json({release_id: id, config: id === rel.id ? manifestConfig : null, files: id === rel.id ? fileList : []});
      }
      const m = path.match(new RegExp(`^/releases/${rel.id}/assets/(.+)$`));
      if (m) {
        const name = m[1].split('/').map(decodeURIComponent).join('/');
        const info = meta[name];
        if (!info) return new Response('not found', {status: 404});
        return Response.json({path: name, sha256: info.sha256, size_bytes: info.size, content_type: info.type, download_url: 'http://assets.test/' + name});
      }
      return new Response('not found', {status: 404});
    },
  };
}

function makeEnv(manifestConfig, fileList, relId = release.id, options = {}) {
  const rel = {...release, id: relId};
  const workerResponses = [];
  const env = {
    POD_NAME: 'pod', POD_UID: 'uid', RUNTIME_TOKEN: 't', MANAGEMENT_TOKEN: 'm', ASSETS_CACHE_MAX: '1048576',
    PLATFORM: platformFor(manifestConfig, fileList, rel, options),
    LOADER: {
      get(key, factory) {
        return {
          getEntrypoint() {
            return {
              async fetch(request) {
                workerResponses.push(new URL(request.url).pathname);
                return Response.json({worker: true, path: new URL(request.url).pathname});
              },
            };
          },
        };
      },
    },
  };
  env.__workerResponses = workerResponses;
  return env;
}

async function call(handler, path, headers = {}) {
  return handler.fetch(new Request('http://w-test.workers.test' + path, {headers}));
}

const fileList = Object.entries(meta).map(([path, m]) => ({path, sha256: m.sha256, size_bytes: m.size, content_type: m.type}));
const env = makeEnv(config, fileList);
const handler = new entry.Private(env, {});

let failures = 0;
async function check(name, run) {
  try { await run(); console.log('  ok  ' + name); }
  catch (e) { failures++; console.log('  FAIL ' + name + ' :: ' + e.message); }
}

await check('run_worker_first positive hits the worker', async () => {
  const r = await call(handler, '/api/hello');
  assert.equal(r.status, 200);
  assert.equal((await r.json()).worker, true);
});
await check('negated pattern skips the worker and serves the asset', async () => {
  const r = await call(handler, '/api/docs/docs');
  assert.equal(r.status, 200, 'expected asset path, got ' + r.status);
});
await check('/ serves index.html', async () => {
  const r = await call(handler, '/');
  assert.equal(r.status, 200);
  assert.match(await r.text(), /<title>root<\/title>/);
  assert.equal(r.headers.get('content-type'), 'text/html; charset=utf-8');
});
await check('/about serves about.html', async () => {
  const r = await call(handler, '/about');
  assert.equal(r.status, 200);
  assert.match(await r.text(), /about page|about<\/h1>/);
});
await check('/about.html redirects 307 to /about', async () => {
  const r = await call(handler, '/about.html');
  assert.equal(r.status, 307);
  assert.equal(r.headers.get('location'), '/about');
});
await check('navigation miss falls back to SPA shell with 200', async () => {
  const r = await call(handler, '/dashboard', {'sec-fetch-mode': 'navigate'});
  assert.equal(r.status, 200);
  assert.match(await r.text(), /<title>root<\/title>/);
});
await check('non-navigation miss goes to the worker', async () => {
  const r = await call(handler, '/dashboard');
  assert.equal(r.status, 200);
  assert.equal((await r.json()).worker, true);
});
await check('asset response carries ETag and 304 on revalidation', async () => {
  const first = await call(handler, '/');
  const etag = first.headers.get('etag');
  assert.ok(etag, 'missing etag');
  const second = await call(handler, '/', {'if-none-match': etag});
  assert.equal(second.status, 304);
});
await check('response advertises release and pod identity', async () => {
  const r = await call(handler, '/');
  assert.equal(r.headers.get('x-krun-release'), release.id);
  assert.equal(r.headers.get('x-krun-pod'), 'uid');
});
await check('hashed asset gets immutable caching', async () => {
  const r = await call(handler, '/assets/app-0123abcd.js');
  assert.equal(r.status, 200);
  assert.match(r.headers.get('cache-control') || '', /immutable/);
});
await check('asset bytes are cached in the LRU (single download)', async () => {
  const before = downloads.filter(d => d === 'index.html').length;
  await call(handler, '/');
  const after = downloads.filter(d => d === 'index.html').length;
  assert.equal(after, before, 'index.html was re-downloaded');
});
await check('services without assets forward everything to the worker', async () => {
  const plainEnv = makeEnv(null, [], 'rel_plain');
  const plainEntry = await freshEntry();
  const plain = new plainEntry.Private(plainEnv, {});
  const r = await call(plain, '/');
  assert.equal(r.status, 200);
  assert.equal((await r.json()).worker, true);
});
await check('non-navigation miss without SPA config goes to the worker', async () => {
  const noSpaEnv = makeEnv({...config, not_found_handling: 'none'}, fileList, 'rel_nospa');
  const noSpaEntry = await freshEntry();
  const noSpa = new noSpaEntry.Private(noSpaEnv, {});
  const r = await call(noSpa, '/nowhere', {'sec-fetch-mode': 'navigate'});
  assert.equal(r.status, 200);
  assert.equal((await r.json()).worker, true);
});
await check('incremental op switches the route without a snapshot refetch', async () => {
  const plain = {...release, id: 'rel_plain'};
  const changes = [{from: 1, to: 2, ops: [
    {type: 'release_put', release: plain},
    {type: 'route_upsert', host: 'w-test.workers.test', route: {host: 'w-test.workers.test', path: '/', enabled: true, release: 'rel_plain'}},
  ]}];
  const incEnv = makeEnv(null, [], release.id, {changes});
  const incEntry = await freshEntry();
  const inc = new incEntry.Private(incEnv, {});
  let switched = false;
  for (let i = 0; i < 60; i++) {
    const r = await call(inc, '/');
    if (r.headers.get('x-krun-release') === 'rel_plain') {
      assert.equal((await r.json()).worker, true);
      switched = true;
      break;
    }
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.ok(switched, 'route never switched to rel_plain');
});
await check('bootstrap reads latest and gzip snapshot from object storage', async () => {
  const relId = 'rel_oss';
  const snapshot = {config_revision: 7, pool: 'default', routes: [{host: 'w-test.workers.test', path: '/', enabled: true, release: relId}], releases: {[relId]: {...release, id: relId}}};
  const compressed = gzipSync(Buffer.from(JSON.stringify(snapshot)));
  configObjects.set('latest.json', encoder.encode(JSON.stringify({config_revision: 7, snapshot: 'snapshots/7.json.gz', sha256: await digest(new Uint8Array(compressed)), ops_floor: 1})));
  configObjects.set('snapshots/7.json.gz', new Uint8Array(compressed));
  const ossEnv = makeEnv(null, [], relId);
  ossEnv.CONFIG_BASE = 'http://config.test';
  const ossEntry = await freshEntry();
  const oss = new ossEntry.Private(ossEnv, {});
  const r = await call(oss, '/');
  assert.equal(r.status, 200);
  assert.equal((await r.json()).worker, true);
  assert.equal(r.headers.get('x-krun-release'), relId);
  assert(downloads.includes('config:latest.json'), 'latest.json was not fetched');
  assert(downloads.includes('config:snapshots/7.json.gz'), 'snapshot was not fetched');
});
await check('readiness starts the follow loop for an idle Pod', async () => {
  const plain = {...release, id: 'rel_idle'};
  const changes = [{from: 1, to: 2, ops: [
    {type: 'release_put', release: plain},
    {type: 'route_upsert', host: 'w-test.workers.test', route: {host: 'w-test.workers.test', path: '/', enabled: true, release: 'rel_idle'}},
  ]}];
  const idleEnv = makeEnv(null, [], release.id, {changes});
  const idleEntry = await freshEntry();
  const management = new idleEntry.Management(idleEnv, {});
  const ready = await management.fetch(new Request('http://pod/ready'));
  assert.equal(ready.status, 200);
  const priv = new idleEntry.Private(idleEnv, {});
  let switched = false;
  for (let i = 0; i < 60; i++) {
    const r = await call(priv, '/');
    if (r.headers.get('x-krun-release') === 'rel_idle') {
      switched = true;
      break;
    }
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.ok(switched, 'idle Pod never applied the incremental revision');
});

console.log(failures === 0 ? 'ALL RUNTIME CHECKS PASSED' : failures + ' CHECK(S) FAILED');
process.exit(failures === 0 ? 0 : 1);
