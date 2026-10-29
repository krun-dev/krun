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
function tarFile(name, bytes) {
  bytes = Buffer.from(bytes);
  const header = Buffer.alloc(512);
  let base = name, prefix = '';
  if (Buffer.byteLength(base, 'utf8') > 100) {
    const parts = name.split('/');
    while (parts.length > 1 && Buffer.byteLength(parts.join('/'), 'utf8') > 100) prefix += (prefix ? '/' : '') + parts.shift();
    base = parts.join('/');
  }
  if (Buffer.byteLength(base, 'utf8') > 100 || Buffer.byteLength(prefix, 'utf8') > 155) throw new Error('test path exceeds ustar');
  header.write(base, 0, 100, 'utf8');
  if (prefix) header.write(prefix, 345, 155, 'utf8');
  const octal = (offset, length, value) => header.write(value.toString(8).padStart(length - 1, '0') + '\0', offset, length, 'ascii');
  octal(100, 8, 0o644); octal(108, 8, 0); octal(116, 8, 0); octal(124, 12, bytes.length); octal(136, 12, 0);
  header.fill(32, 148, 156); header[156] = 48; header.write('ustar\0', 257, 6, 'ascii'); header.write('00', 263, 2, 'ascii');
  const sum = header.reduce((value, byte) => value + byte, 0);
  header.write(sum.toString(8).padStart(6, '0') + '\0 ', 148, 8, 'ascii');
  return Buffer.concat([header, bytes, Buffer.alloc((512 - bytes.length % 512) % 512)]);
}
function artifactArchive(manifest, moduleFiles, assetFiles = {}) {
  const parts = [tarFile('manifest.json', JSON.stringify(manifest))];
  for (const [name, bytes] of Object.entries(moduleFiles)) parts.push(tarFile('modules/' + name, bytes));
  for (const [name, bytes] of Object.entries(assetFiles)) parts.push(tarFile('assets/' + name, bytes));
  return new Uint8Array(gzipSync(Buffer.concat([...parts, Buffer.alloc(1024)])));
}

const release = {id: 'rel_test', service_id: 'svc', organization_id: 'org', environment: 'production', artifact_id: 'art', archive_sha256: 'a'.repeat(64), module_sha256: 'b'.repeat(64), variables: {}, pool: 'default'};
const files = {
  'index.html': '<!doctype html><title>root</title>',
  'about.html': '<!doctype html><h1>about</h1>',
  'docs.html': '<!doctype html><h1>docs</h1>',
  'assets/app-0123abcd.js': 'console.log("hashed")',
  'LICENSE': 'runtime fixture license',
};
const meta = {};
for (const [name, text] of Object.entries(files)) {
  meta[name] = {sha256: await digest(text), size: text.length, type: name.endsWith('.js') ? 'application/javascript; charset=utf-8' : name.endsWith('.html') ? 'text/html; charset=utf-8' : 'application/octet-stream'};
}
const config = {directory: 'public', run_worker_first: ['/api/*', '!/api/docs/*'], not_found_handling: 'single-page-application', html_handling: 'auto-trailing-slash'};

const downloads = [];
const configObjects = new Map();
const artifactObjects = new Map();
globalThis.fetch = async (url) => {
  const raw = String(url);
  if (raw.startsWith('http://config.test/')) {
    const key = raw.replace('http://config.test/', '');
    downloads.push('config:' + key);
    if (!configObjects.has(key)) return new Response('missing', {status: 404});
    return new Response(configObjects.get(key), {status: 200});
  }
  if (raw.startsWith('http://artifacts.test/')) {
    const key = raw.replace('http://artifacts.test/', '');
    if (!artifactObjects.has(key)) return new Response('missing', {status: 404});
    return new Response(artifactObjects.get(key), {status: 200});
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
      if (path === '/artifacts/' + rel.artifact_id && options.artifact) {
        return Response.json(options.artifact);
      }
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
  const rel = {...release, ...(options.release || {}), id: relId};
  const workerResponses = [];
  const env = {
    POD_NAME: 'pod', POD_UID: 'uid', RUNTIME_TOKEN: 't', MANAGEMENT_TOKEN: 'm', ASSETS_CACHE_MAX: '1048576',
    PLATFORM: platformFor(manifestConfig, fileList, rel, options),
  };
  let loaded;
  env.LOADER = {
    get(key, factory) {
      if (options.exerciseLoader && !loaded) loaded = Promise.resolve().then(factory);
      env.__loaderKey = key;
      env.__loaderConfig = loaded;
      return {
        getEntrypoint() {
          return {
            async ping() { if (loaded) await loaded; return {ok: true}; },
            async fetch(request) {
              if (loaded) await loaded;
              workerResponses.push(new URL(request.url).pathname);
              return Response.json({worker: true, path: new URL(request.url).pathname});
            },
          };
        },
      };
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
await check('extensionless asset is opaque and disables content sniffing', async () => {
  const r = await call(handler, '/LICENSE');
  assert.equal(r.status, 200);
  assert.equal(await r.text(), 'runtime fixture license');
  assert.equal(r.headers.get('content-type'), 'application/octet-stream');
  assert.equal(r.headers.get('x-content-type-options'), 'nosniff');
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

await check('profile v2 loads typed modules, compatibility config and an isolated assets binding', async () => {
  const nestedName = 'nested/'.repeat(20) + 'message.txt';
  const moduleFiles = {
    'index.js': 'export default {fetch(){return new Response("ok")}}',
    // Module type is declared by Wrangler rules, not inferred from extension.
    'helper.js': 'module.exports = {value: 1};',
    'message.html': 'hello',
    'blob.dat': new Uint8Array([1, 2, 3]),
    'module.payload': new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]),
    'index.metadata': '{"version":3}',
    [nestedName]: 'nested module',
  };
  const types = { 'index.js': 'esm', 'helper.js': 'commonjs', 'message.html': 'text', 'blob.dat': 'data', 'module.payload': 'wasm', 'index.metadata': 'source-map', [nestedName]: 'text' };
  const modules = [];
  for (const [name, value] of Object.entries(moduleFiles)) {
    const bytes = typeof value === 'string' ? encoder.encode(value) : value;
    modules.push({name, path: 'modules/' + name, type: types[name], sha256: await digest(bytes), size_bytes: bytes.length});
  }
  const manifestConfig = {...config, binding: 'ASSETS'};
  const assetManifestFiles = fileList.map(file => ({path: file.path, sha256: file.sha256, size_bytes: file.size_bytes, content_type: file.content_type}));
  const manifest = {schema_version: 2, profile: 'workerd-bundle-v2', compatibility_date: '2026-09-28', compatibility_flags: ['nodejs_compat'], main_module: 'index.js', modules, assets: {...manifestConfig, files: assetManifestFiles}};
  const archive = artifactArchive(manifest, moduleFiles, files);
  artifactObjects.set('v2.tar.gz', archive);
  const v2Release = {...release, profile: manifest.profile, compatibility_date: manifest.compatibility_date, compatibility_flags: manifest.compatibility_flags, module_sha256: modules[0].sha256, archive_sha256: await digest(archive), variables: {MODE: 'test'}};
  const artifact = {artifact_id: 'art', archive_sha256: v2Release.archive_sha256, size: archive.length, download_url: 'http://artifacts.test/v2.tar.gz'};
  const v2Env = makeEnv(manifestConfig, fileList, 'rel_v2', {exerciseLoader: true, release: v2Release, artifact});
  const v2Entry = await freshEntry();
  const ctx = {exports: {ReleaseAssets: ({props}) => new v2Entry.ReleaseAssets(v2Env, {props})}};
  const v2 = new v2Entry.Private(v2Env, ctx);
  const response = await call(v2, '/api/load');
  assert.equal(response.status, 200);
  const loaded = await v2Env.__loaderConfig;
  assert.match(v2Env.__loaderKey, /workerd-bundle-v2$/);
  assert.equal(loaded.compatibilityDate, '2026-09-28');
  assert.deepEqual(loaded.compatibilityFlags, ['nodejs_compat']);
  assert.equal(loaded.mainModule, 'index.js');
  assert.equal(loaded.modules['index.js'].js, moduleFiles['index.js']);
  assert.equal(loaded.modules['helper.js'].cjs, moduleFiles['helper.js']);
  assert.equal(loaded.modules['message.html'].text, 'hello');
  assert.equal(loaded.modules[nestedName].text, 'nested module');
  assert.deepEqual([...new Uint8Array(loaded.modules['blob.dat'].data)], [1, 2, 3]);
  assert.ok(loaded.modules['module.payload'].wasm instanceof ArrayBuffer);
  assert.equal(loaded.modules['index.metadata'], undefined);
  assert.equal(Object.getPrototypeOf(loaded.env), Object.prototype);
  assert.equal(loaded.env.MODE, 'test');
  assert.equal(loaded.env.RUNTIME_TOKEN, undefined);
  assert.ok(loaded.env.ASSETS, 'ASSETS binding missing');
  const asset = await loaded.env.ASSETS.fetch(new Request('https://assets.local/about'));
  assert.equal(asset.status, 200);
  assert.match(await asset.text(), /about/);
});

await check('prototype-magic asset binding names are rejected before WorkerLoader', async () => {
  const module = 'export default {fetch(){return new Response("ok")}}';
  const moduleDigest = await digest(module);
  const magicConfig = {...config, binding: '__proto__', not_found_handling: 'none'};
  const manifest = {
    schema_version: 2,
    profile: 'workerd-bundle-v2',
    compatibility_date: '2026-09-28',
    compatibility_flags: [],
    main_module: 'index.js',
    modules: [{name: 'index.js', path: 'modules/index.js', type: 'esm', sha256: moduleDigest, size_bytes: module.length}],
    assets: {...magicConfig, files: [{path: 'index.html', sha256: meta['index.html'].sha256, size_bytes: meta['index.html'].size, content_type: meta['index.html'].type}]},
  };
  const archive = artifactArchive(manifest, {'index.js': module}, {'index.html': files['index.html']});
  artifactObjects.set('magic-binding.tar.gz', archive);
  const magicRelease = {...release, id: 'rel_magic', profile: manifest.profile, compatibility_date: manifest.compatibility_date, compatibility_flags: [], module_sha256: moduleDigest, archive_sha256: await digest(archive), variables: {MODE: 'magic'}};
  const artifact = {artifact_id: 'art', archive_sha256: magicRelease.archive_sha256, size: archive.length, download_url: 'http://artifacts.test/magic-binding.tar.gz'};
  const magicEnv = makeEnv(magicConfig, [fileList[0]], magicRelease.id, {exerciseLoader: true, release: magicRelease, artifact});
  const magicEntry = await freshEntry();
  const ctx = {exports: {ReleaseAssets: ({props}) => new magicEntry.ReleaseAssets(magicEnv, {props})}};
  const response = await call(new magicEntry.Private(magicEnv, ctx), '/api/load');
  assert.equal(response.status, 503);
});

await check('asset binding cannot replace variables or reserved platform bindings', async () => {
  const module = 'export default {fetch(){return new Response("ok")}}';
  const moduleDigest = await digest(module);
  const manifest = {schema_version: 1, profile: 'workerd-bundle-v2', compatibility_date: '2026-09-28', compatibility_flags: [], main_module: 'index.js', modules: [{name: 'index.js', path: 'modules/index.js', type: 'esm', sha256: moduleDigest, size_bytes: module.length}]};
  // Schema 1 has no binding, so use a schema 2 artifact with one asset.
  manifest.schema_version = 2;
  manifest.assets = {directory: 'public', binding: 'ASSETS', run_worker_first: ['/api/*'], not_found_handling: 'none', html_handling: 'none', files: [{path: 'index.html', sha256: meta['index.html'].sha256, size_bytes: meta['index.html'].size, content_type: meta['index.html'].type}]};
  const archive = artifactArchive(manifest, {'index.js': module}, {'index.html': files['index.html']});
  artifactObjects.set('collision.tar.gz', archive);
  const collisionRelease = {...release, profile: manifest.profile, compatibility_date: manifest.compatibility_date, compatibility_flags: [], module_sha256: moduleDigest, archive_sha256: await digest(archive), variables: {ASSETS: 'attacker'}};
  const artifact = {artifact_id: 'art', archive_sha256: collisionRelease.archive_sha256, size: archive.length, download_url: 'http://artifacts.test/collision.tar.gz'};
  const collisionEnv = makeEnv(manifest.assets, [fileList[0]], 'rel_collision', {exerciseLoader: true, release: collisionRelease, artifact});
  const collisionEntry = await freshEntry();
  const ctx = {exports: {ReleaseAssets: ({props}) => new collisionEntry.ReleaseAssets(collisionEnv, {props})}};
  const response = await call(new collisionEntry.Private(collisionEnv, ctx), '/api/load');
  assert.equal(response.status, 503);
});

await check('candidate validation rejects undeclared artifact entries', async () => {
  const module = 'export default {fetch(){return new Response("ok")}}';
  const moduleDigest = await digest(module);
  const manifest = {schema_version: 1, profile: 'workerd-bundle-v2', compatibility_date: '2026-09-28', compatibility_flags: [], main_module: 'index.js', modules: [{name: 'index.js', path: 'modules/index.js', type: 'esm', sha256: moduleDigest, size_bytes: module.length}]};
  const parts = [tarFile('manifest.json', JSON.stringify(manifest)), tarFile('modules/index.js', module), tarFile('modules/undeclared.js', 'bad'), Buffer.alloc(1024)];
  const archive = new Uint8Array(gzipSync(Buffer.concat(parts)));
  artifactObjects.set('invalid.tar.gz', archive);
  const invalidRelease = {...release, profile: manifest.profile, compatibility_date: manifest.compatibility_date, compatibility_flags: [], module_sha256: moduleDigest, archive_sha256: await digest(archive)};
  const invalidArtifact = {artifact_id: 'art', archive_sha256: invalidRelease.archive_sha256, size: archive.length, download_url: 'http://artifacts.test/invalid.tar.gz'};
  const invalidEnv = makeEnv(null, [], 'rel_invalid', {exerciseLoader: true, release: invalidRelease});
  const invalidEntry = await freshEntry();
  const management = new invalidEntry.Management(invalidEnv, {exports: {}});
  const response = await management.fetch(new Request('http://pod/validate', {method: 'POST', headers: {authorization: 'Bearer m', 'content-type': 'application/json'}, body: JSON.stringify({release: invalidRelease, artifact: invalidArtifact})}));
  assert.equal(response.status, 503);
});

await check('candidate validation rejects malformed typed module payloads', async () => {
  const main = 'export default {fetch(){return new Response("ok")}}';
  const mainDigest = await digest(main);
  const malformed = [
    {key: 'bad-wasm', name: 'parser.payload', type: 'wasm', bytes: new Uint8Array([1, 2, 3, 4])},
    {key: 'bad-map', name: 'index.metadata', type: 'source-map', bytes: 'not-json'},
    {key: 'bad-name', name: 'bad\nname.js', type: 'esm', bytes: 'export default 1'},
  ];
  for (const item of malformed) {
    const bytes = typeof item.bytes === 'string' ? encoder.encode(item.bytes) : item.bytes;
    const manifest = {
      schema_version: 1,
      profile: 'workerd-bundle-v2',
      compatibility_date: '2026-09-28',
      compatibility_flags: [],
      main_module: 'index.js',
      modules: [
        {name: 'index.js', path: 'modules/index.js', type: 'esm', sha256: mainDigest, size_bytes: main.length},
        {name: item.name, path: 'modules/' + item.name, type: item.type, sha256: await digest(bytes), size_bytes: bytes.length},
      ],
    };
    const archive = artifactArchive(manifest, {'index.js': main, [item.name]: bytes});
    artifactObjects.set(item.key + '.tar.gz', archive);
    const invalidRelease = {...release, id: 'rel_' + item.key, profile: manifest.profile, compatibility_date: manifest.compatibility_date, compatibility_flags: [], module_sha256: mainDigest, archive_sha256: await digest(archive)};
    const invalidArtifact = {artifact_id: 'art', archive_sha256: invalidRelease.archive_sha256, size: archive.length, download_url: 'http://artifacts.test/' + item.key + '.tar.gz'};
    const invalidEnv = makeEnv(null, [], invalidRelease.id, {exerciseLoader: true, release: invalidRelease});
    const invalidEntry = await freshEntry();
    const management = new invalidEntry.Management(invalidEnv, {exports: {}});
    const response = await management.fetch(new Request('http://pod/validate', {method: 'POST', headers: {authorization: 'Bearer m', 'content-type': 'application/json'}, body: JSON.stringify({release: invalidRelease, artifact: invalidArtifact})}));
    assert.equal(response.status, 503, item.key + ' was accepted');
  }
});

console.log(failures === 0 ? 'ALL RUNTIME CHECKS PASSED' : failures + ' CHECK(S) FAILED');
process.exit(failures === 0 ? 0 : 1);
