import assert from 'node:assert/strict';
import {mkdtempSync, mkdirSync, rmSync, writeFileSync} from 'node:fs';
import {createServer} from 'node:http';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';
import {gzipSync} from 'node:zlib';

function tarFile(name, bytes) {
  const header = Buffer.alloc(512);
  header.write(name, 0, 100, 'utf8');
  const octal = (offset, length, value) => header.write(value.toString(8).padStart(length - 1, '0') + '\0', offset, length, 'ascii');
  octal(100, 8, 0o644); octal(108, 8, 0); octal(116, 8, 0); octal(124, 12, bytes.length); octal(136, 12, 0);
  header.fill(32, 148, 156); header[156] = 48; header.write('ustar\0', 257, 6, 'ascii'); header.write('00', 263, 2, 'ascii');
  const sum = header.reduce((total, byte) => total + byte, 0);
  header.write(sum.toString(8).padStart(6, '0') + '\0 ', 148, 8, 'ascii');
  return Buffer.concat([header, bytes, Buffer.alloc((512 - bytes.length % 512) % 512)]);
}

const duplicate = gzipSync(Buffer.concat([
  tarFile('src/index.js', Buffer.from('export default {fetch(){return new Response("one")}}')),
  tarFile('src/index.js', Buffer.from('export default {fetch(){return new Response("two")}}')),
  Buffer.alloc(1024),
]));
const bomb = gzipSync(Buffer.alloc(25 * 1024 * 1024));
const noNodeCompat = gzipSync(Buffer.concat([
  tarFile('src/index.js', Buffer.from('import {createHash} from "crypto";export default {fetch(){return new Response(createHash("sha256").update("ok").digest("hex"))}}')),
  Buffer.alloc(1024),
]));
const overflowChunk = Buffer.alloc(1024 * 1024);
let uploads = 0;
const server = createServer((request, response) => {
  if (request.url === '/duplicate') {
    response.end(duplicate);
  } else if (request.url === '/bomb') {
    response.end(bomb);
  } else if (request.url === '/oversized-length') {
    response.writeHead(200, {'content-length': String(32 * 1024 * 1024 + 1)});
    response.flushHeaders();
    setTimeout(() => response.end(), 100);
  } else if (request.url === '/stream-overflow') {
    response.writeHead(200, {'content-type': 'application/gzip'});
    for (let i = 0; i < 33; i++) response.write(overflowChunk);
    response.end();
  } else if (request.url === '/no-node-compat') {
    response.end(noNodeCompat);
  } else {
    uploads++;
    response.end();
  }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const port = server.address().port;
const scratch = mkdtempSync(path.join(tmpdir(), 'krun-builder-security-'));
const here = path.dirname(fileURLToPath(import.meta.url));

async function rejected(name, expected) {
  const root = path.join(scratch, name);
  const inputDir = path.join(root, 'input');
  mkdirSync(inputDir, {recursive: true});
  const input = {
    entrypoint: 'index.js',
    compatibility_date: '2026-09-28',
    compatibility_flags: [],
    source_url: `http://127.0.0.1:${port}/${name}`,
    upload_url: `http://127.0.0.1:${port}/upload`,
  };
  const inputPath = path.join(inputDir, 'input.json');
  writeFileSync(inputPath, JSON.stringify(input));
  const result = await new Promise(resolve => {
    const child = spawn('node', [path.join(here, '..', 'build-worker.mjs')], {
      cwd: path.join(here, '..'),
      env: {...process.env, KRUN_BUILD_INPUT: inputPath, KRUN_BUILD_WORK: path.join(root, 'work')},
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let output = '';
    child.stdout.on('data', chunk => { output += chunk; });
    child.stderr.on('data', chunk => { output += chunk; });
    child.once('close', code => resolve({code, output}));
  });
  assert.notEqual(result.code, 0, `${name} unexpectedly built`);
  assert.match(result.output, expected);
  console.log('  ok  rejected ' + name);
}

try {
  await rejected('duplicate', /unique regular files/);
  await rejected('bomb', /Cannot create a Buffer larger than|larger than maxOutputLength|buffer too large/i);
  await rejected('oversized-length', /Source bundle download is too large/);
  await rejected('stream-overflow', /Source bundle download is too large/);
  await rejected('no-node-compat', /Node\.js builtin imports require the nodejs_compat compatibility flag/);
  assert.equal(uploads, 0, 'rejected sources must not reach artifact upload');
  console.log('ALL BUILDER SECURITY CHECKS PASSED');
} finally {
  server.close();
  rmSync(scratch, {recursive: true, force: true});
}
