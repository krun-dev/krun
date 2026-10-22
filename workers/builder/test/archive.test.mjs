// Executes workers/builder/build-worker.mjs against a real local HTTP server and
// verifies the schema-2 archive it produces with the control-plane parser rules.
import {mkdirSync, writeFileSync, readFileSync, mkdtempSync, realpathSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer} from 'node:http';
import {spawn} from 'node:child_process';
import {gzipSync} from 'node:zlib';
import {createHash} from 'node:crypto';
import assert from 'node:assert/strict';

const sha = raw => createHash('sha256').update(raw).digest('hex');

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

const code = {
  'index.ts': `export default {async fetch(request, env){const url=new URL(request.url);if(url.pathname==='/api/hello')return Response.json({hello:true,release:env.KRUN_RELEASE_ID});return Response.json({path:url.pathname})}}`,
};
const assets = {
  'index.html': '<!doctype html><title>builder</title>',
  'about.html': '<!doctype html><h1>about</h1>',
  'assets/app-0123abcd.js': 'console.log("x")',
  'assets/style.css': 'body{color:red}',
};
const bundle = gzipSync(Buffer.concat([
  ...Object.entries(code).map(([n, t]) => tarFile('src/' + n, Buffer.from(t))),
  ...Object.entries(assets).map(([n, t]) => tarFile('assets/' + n, Buffer.from(t))),
  Buffer.alloc(1024),
]), {level: 6});

let uploaded = null;
const server = createServer((req, res) => {
  const chunks = [];
  req.on('data', c => chunks.push(c));
  req.on('end', () => {
    if (req.url.startsWith('/source')) {
      res.writeHead(200, {'content-type': 'application/gzip'});
      res.end(bundle);
      return;
    }
    uploaded = Buffer.concat(chunks);
    res.writeHead(200); res.end();
  });
});
await new Promise(r => server.listen(0, '127.0.0.1', r));
const port = server.address().port;

// realpathSync matters on macOS, where /tmp is a symlink and esbuild's
// boundary check compares resolved paths.
const scratch = realpathSync(mkdtempSync(join(tmpdir(), 'krun-builder-')));
const inputPath = join(scratch, 'input', 'input.json');
const workDir = join(scratch, 'work');
const entryStub = join(scratch, 'work', '__krun_entry__.mjs');
mkdirSync(join(scratch, 'input'), {recursive: true});
mkdirSync(workDir, {recursive: true});
const input = {
  entrypoint: 'index.ts',
  source_url: `http://127.0.0.1:${port}/source`,
  assets: {directory: 'public', run_worker_first: ['/api/*'], not_found_handling: 'single-page-application', html_handling: 'auto-trailing-slash'},
  upload_url: `http://127.0.0.1:${port}/upload`,
};

// The builder hardcodes /input, /work, /tmp paths (in-container layout).
// Rewrite those prefixes to the local scratch dirs for this run only.
const here = dirname(fileURLToPath(import.meta.url));
const builderSource = readFileSync(join(here, '..', 'build-worker.mjs'), 'utf8')
  .replaceAll("'/input/input.json'", JSON.stringify(inputPath))
  .replaceAll("'/work/src/__krun_entry__.mjs'", JSON.stringify(entryStub))
  .replaceAll("'/work/src'", JSON.stringify(workDir));
writeFileSync(inputPath, JSON.stringify(input));
// Run the copy from inside the builder package so `import 'esbuild'` resolves.
const runner = join(here, '..', '.build-worker.test.mjs');
writeFileSync(runner, builderSource);

// Spawn (not execFileSync): the stub server lives in this process and must keep
// serving the source bundle while the builder runs.
const exitCode = await new Promise((resolve) => {
  const child = spawn('node', [runner], {cwd: scratch, stdio: ['ignore', 'pipe', 'pipe']});
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
check('assets config preserved', () => {
  assert.equal(manifest.assets.directory, 'public');
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
});
check('archive contains no undeclared entries', () => {
  const expected = 2 + manifest.assets.files.length;
  assert.equal(Object.keys(entries).length, expected, Object.keys(entries).join(','));
});
check('content types assigned per extension', () => {
  const byPath = new Map(manifest.assets.files.map(f => [f.path, f.content_type]));
  assert.equal(byPath.get('index.html'), 'text/html; charset=utf-8');
  assert.equal(byPath.get('assets/app-0123abcd.js'), 'application/javascript; charset=utf-8');
  assert.equal(byPath.get('assets/style.css'), 'text/css; charset=utf-8');
});

try { rmSync(runner, {force: true}); } catch {}
console.log(failures === 0 ? 'ALL BUILDER CHECKS PASSED' : failures + ' CHECK(S) FAILED');
process.exit(failures === 0 ? 0 : 1);
