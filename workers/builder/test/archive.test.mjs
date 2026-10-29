// Executes workers/builder/build-worker.mjs against a real local HTTP server and
// verifies the schema-2 archive it produces with the control-plane parser rules.
import {mkdirSync, writeFileSync, readFileSync, mkdtempSync, realpathSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer} from 'node:http';
import {execFileSync, spawn} from 'node:child_process';
import {gzipSync} from 'node:zlib';
import {createHash} from 'node:crypto';
import assert from 'node:assert/strict';

const sha = raw => createHash('sha256').update(raw).digest('hex');
const here = dirname(fileURLToPath(import.meta.url));
// realpathSync matters on macOS, where /tmp is a symlink and esbuild's
// boundary check compares resolved paths.
const scratch = realpathSync(mkdtempSync(join(tmpdir(), 'krun-builder-')));
const packHome = join(scratch, 'pack-home');
const packUserConfig = join(packHome, 'user.npmrc');
const packGlobalConfig = join(packHome, 'global.npmrc');
mkdirSync(packHome, {recursive: true});
writeFileSync(packUserConfig, '');
writeFileSync(packGlobalConfig, '');
const packed = JSON.parse(execFileSync('npm', [
  'pack', join(here, '..', 'node_modules', 'hono'), '--ignore-scripts',
  '--pack-destination', scratch, '--json', '--cache', join(scratch, 'pack-cache'),
  '--userconfig', packUserConfig, '--globalconfig', packGlobalConfig,
], {encoding: 'utf8', env: {PATH: process.env.PATH || '/usr/local/bin:/usr/bin:/bin', HOME: packHome}}));
const packageArchive = readFileSync(join(scratch, packed[0].filename));

function tarFile(name, bytes) {
  const h = Buffer.alloc(512);
  h.write(name, 0, 100, 'utf8');
  const o = (off, len, n) => h.write(n.toString(8).padStart(len - 1, '0') + '\0', off, len, 'ascii');
  o(100, 8, 0o644); o(108, 8, 0); o(116, 8, 0); o(124, 12, bytes.length); o(136, 12, 0);
  h.fill(32, 148, 156); h[156] = 48; h.write('ustar\0', 257, 6, 'ascii'); h.write('00', 263, 2, 'ascii');
  const sum = h.reduce((n, b) => n + b, 0);
  h.write(sum.toString(8).padStart(6, '0') + '\0 ', 148, 8, 'ascii');
  return Buffer.concat([h, bytes, Buffer.alloc((512 - bytes.length % 512) % 512)]);
}

const assets = {
  'index.html': '<!doctype html><title>builder</title>',
  'about.html': '<!doctype html><h1>about</h1>',
  'assets/app-0123abcd.js': 'console.log("x")',
  'assets/style.css': 'body{color:red}',
  'LICENSE': 'fixture license',
};

let uploaded = null;
let bundle = null;
let registryAuthorization = null;
const server = createServer((req, res) => {
  const chunks = [];
  req.on('data', c => chunks.push(c));
  req.on('end', () => {
    if (req.url.startsWith('/source')) {
      res.writeHead(200, {'content-type': 'application/gzip'});
      res.end(bundle);
      return;
    }
    if (req.url === '/registry/hono/-/hono-4.13.14.tgz') {
      registryAuthorization = req.headers.authorization || null;
      res.writeHead(200, {'content-type': 'application/octet-stream'});
      res.end(packageArchive);
      return;
    }
    uploaded = Buffer.concat(chunks);
    res.writeHead(200); res.end();
  });
});
await new Promise(r => server.listen(0, '127.0.0.1', r));
const port = server.address().port;
const hostileHome = join(scratch, 'host-home');
const emptyPath = join(scratch, 'empty-path');
const hostileNpmrc = join(hostileHome, '.npmrc');
mkdirSync(hostileHome, {recursive: true});
mkdirSync(emptyPath, {recursive: true});
writeFileSync(hostileNpmrc, `//127.0.0.1:${port}/registry/:_authToken=ambient-secret\nalways-auth=true\n`);
const packageIntegrity = 'sha512-' + createHash('sha512').update(packageArchive).digest('base64');
const packageURL = `http://127.0.0.1:${port}/registry/hono/-/hono-4.13.14.tgz`;
const code = {
  'index.ts': `import {Hono} from 'hono';import {createHash} from 'crypto';import {promisify} from 'node:util';const app=new Hono();app.get('/api/hello',c=>c.json({hello:true,framework:'hono',hash:createHash('sha256').update('ok').digest('hex'),promisify:typeof promisify,release:c.env.KRUN_RELEASE_ID}));app.all('*',c=>c.json({path:c.req.path}));export default app;`,
  'package.json': JSON.stringify({name: 'fixture', version: '1.0.0', private: true, dependencies: {hono: '4.13.14'}}),
  'package-lock.json': JSON.stringify({name: 'fixture', version: '1.0.0', lockfileVersion: 3, requires: true, packages: {'': {name: 'fixture', version: '1.0.0', dependencies: {hono: '4.13.14'}}, 'node_modules/hono': {version: '4.13.14', resolved: packageURL, integrity: packageIntegrity}}}),
};
bundle = gzipSync(Buffer.concat([
  ...Object.entries(code).map(([n, t]) => tarFile('src/' + n, Buffer.from(t))),
  ...Object.entries(assets).map(([n, t]) => tarFile('assets/' + n, Buffer.from(t))),
  Buffer.alloc(1024),
]), {level: 6});

const inputPath = join(scratch, 'input', 'input.json');
const workDir = join(scratch, 'work');
mkdirSync(join(scratch, 'input'), {recursive: true});
mkdirSync(workDir, {recursive: true});
const input = {
  entrypoint: 'index.ts',
  compatibility_date: '2026-09-28',
  compatibility_flags: ['nodejs_compat'],
  source_url: `http://127.0.0.1:${port}/source`,
  assets: {directory: 'public', binding: 'ASSETS', run_worker_first: ['/api/*'], not_found_handling: 'single-page-application', html_handling: 'auto-trailing-slash'},
  upload_url: `http://127.0.0.1:${port}/upload`,
};
writeFileSync(inputPath, JSON.stringify(input));

// Spawn (not execFileSync): the stub server lives in this process and must keep
// serving the source bundle while the builder runs.
const exitCode = await new Promise((resolve) => {
  const child = spawn(process.execPath, [join(here, '..', 'build-worker.mjs')], {
    cwd: join(here, '..'),
    env: {...process.env, PATH: emptyPath, HOME: hostileHome, npm_config_userconfig: hostileNpmrc, KRUN_NPM_CLI: join(scratch, 'must-not-run'), KRUN_BUILD_INPUT: inputPath, KRUN_BUILD_WORK: workDir, KRUN_NPM_REGISTRY: `http://127.0.0.1:${port}/registry/`},
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stdout = '', stderr = '';
  child.stdout.on('data', d => { stdout += d; process.stdout.write('  ' + d); });
  child.stderr.on('data', d => { stderr += d; });
  child.on('close', code => {
    if (stderr.trim()) console.error(stderr.trim());
    resolve(code);
  });
});
if (exitCode !== 0) {
  console.error('BUILDER FAILED (exit ' + exitCode + ')');
  server.close();
  process.exit(1);
}
server.close();
assert.ok(uploaded, 'builder produced no archive');
writeFileSync(join(scratch, 'archive.tar.gz'), uploaded);
if (process.env.KRUN_BUILDER_FIXTURE_OUTPUT) {
  writeFileSync(process.env.KRUN_BUILDER_FIXTURE_OUTPUT, uploaded);
}
console.log('  archive: ' + join(scratch, 'archive.tar.gz'));

// Independent verification of the produced archive (mirrors parseWorkerArchive).
const {gunzipSync} = await import('node:zlib');
const raw = gunzipSync(uploaded);
const entries = {};
for (let off = 0; off + 512 <= raw.length;) {
  const name = raw.subarray(off, off + 100).toString('utf8').replace(/\0.*$/, '');
  if (!name) break;
  const size = parseInt(raw.subarray(off + 124, off + 136).toString('ascii').replace(/[\0 ].*$/, '').trim(), 8) || 0;
  entries[name] = raw.subarray(off + 512, off + 512 + size);
  off += 512 + Math.ceil(size / 512) * 512;
}
const manifest = JSON.parse(entries['manifest.json'].toString('utf8'));
let failures = 0;
const check = (name, fn) => { try { fn(); console.log('  ok  ' + name); } catch (e) { failures++; console.log('  FAIL ' + name + ' :: ' + e.message); } };

check('manifest schema 2', () => assert.equal(manifest.schema_version, 2));
check('v2 profile and runtime compatibility are preserved', () => {
  assert.equal(manifest.profile, 'workerd-bundle-v2');
  assert.equal(manifest.compatibility_date, '2026-09-28');
  assert.deepEqual(manifest.compatibility_flags, ['nodejs_compat']);
});
check('assets config preserved', () => {
  assert.equal(manifest.assets.directory, 'public');
  assert.equal(manifest.assets.binding, 'ASSETS');
  assert.deepEqual(manifest.assets.run_worker_first, ['/api/*']);
  assert.equal(manifest.assets.not_found_handling, 'single-page-application');
  assert.equal(manifest.assets.html_handling, 'auto-trailing-slash');
});
check('asset list is complete and hash-correct', () => {
  const declared = new Map(manifest.assets.files.map(f => [f.path, f]));
  assert.equal(declared.size, Object.keys(assets).length);
  for (const [name, text] of Object.entries(assets)) {
    const f = declared.get(name);
    assert.ok(f, 'missing declaration ' + name);
    const bytes = entries['assets/' + name];
    assert.ok(bytes, 'missing archive entry ' + name);
    assert.equal(f.sha256, sha(bytes));
    assert.equal(f.size_bytes, bytes.length);
    assert.equal(bytes.toString('utf8'), text);
  }
});
check('module digest matches manifest', () => {
  const module = entries['modules/index.js'];
  const m = manifest.modules[0];
  assert.equal(m.sha256, sha(module));
  assert.equal(m.size_bytes, module.length);
  assert.ok(module.toString('utf8').includes('KrunValidation'));
  assert.ok(module.toString('utf8').includes('framework'));
  assert.ok(module.toString('utf8').includes('hono'));
  assert.ok(module.toString('utf8').includes('from "crypto"'));
  assert.ok(module.toString('utf8').includes('from "node:util"'));
});
check('npm ignores ambient user config and auth credentials', () => assert.equal(registryAuthorization, null));
check('archive contains no undeclared entries', () => {
  const expected = 2 + manifest.assets.files.length;
  assert.equal(Object.keys(entries).length, expected, Object.keys(entries).join(','));
});
check('content types assigned per extension', () => {
  const byPath = new Map(manifest.assets.files.map(f => [f.path, f.content_type]));
  assert.equal(byPath.get('index.html'), 'text/html; charset=utf-8');
  assert.equal(byPath.get('assets/app-0123abcd.js'), 'application/javascript; charset=utf-8');
  assert.equal(byPath.get('assets/style.css'), 'text/css; charset=utf-8');
  assert.equal(byPath.get('LICENSE'), 'application/octet-stream');
});

rmSync(scratch, {recursive: true, force: true});
console.log(failures === 0 ? 'ALL BUILDER CHECKS PASSED' : failures + ' CHECK(S) FAILED');
process.exit(failures === 0 ? 0 : 1);
