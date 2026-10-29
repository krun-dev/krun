import assert from 'node:assert/strict';
import {mkdtempSync, rmSync, writeFileSync} from 'node:fs';
import net from 'node:net';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';

const here = path.dirname(fileURLToPath(import.meta.url));
const runtimeRoot = path.resolve(here, '..');
const workerd = path.join(runtimeRoot, 'node_modules', '.bin', 'workerd');
const scratch = mkdtempSync(path.join(tmpdir(), 'krun-workerd-smoke-'));

const port = await new Promise((resolve, reject) => {
  const server = net.createServer();
  server.once('error', reject);
  server.listen(0, '127.0.0.1', () => {
    const value = server.address().port;
    server.close(error => error ? reject(error) : resolve(value));
  });
});

const dynamicSource = `
import {WorkerEntrypoint} from 'cloudflare:workers';
import message from './message.html';
import legacy from './legacy.js';
import payload from './payload.dat';
import compiled from './module.payload';
import {createHash} from 'crypto';
const typed = () => ({
  message,
  legacy: legacy.value,
  data: [...new Uint8Array(payload)],
  wasm: compiled instanceof WebAssembly.Module,
});
export class Named extends WorkerEntrypoint {
  async fetch() {
    return Response.json({named: true, ...typed()});
  }
}
export default {
  async fetch(request, env) {
    const asset = await env.ASSETS.fetch(request);
    return Response.json({
      ...typed(),
      hash: createHash('sha256').update('ok').digest('hex'),
      asset: await asset.text(),
    });
  },
};
`;

const entrySource = `
import {WorkerEntrypoint} from 'cloudflare:workers';
export class ReleaseAssets extends WorkerEntrypoint {
  async fetch() {
    return new Response('asset:' + this.ctx.props.release_id);
  }
}
export default class extends WorkerEntrypoint {
  async fetch(request) {
    const assets = this.ctx.exports.ReleaseAssets({props: {release_id: 'rel-smoke'}});
    const worker = await this.env.LOADER.get('krun-v2-smoke', async () => ({
      compatibilityDate: '2026-09-28',
      compatibilityFlags: ['nodejs_compat'],
      mainModule: 'index.js',
      modules: {
        'index.js': {js: ${JSON.stringify(dynamicSource)}},
        'message.html': {text: 'typed-module-ok'},
        'legacy.js': {cjs: 'module.exports = {value: 7}'},
        'payload.dat': {data: new Uint8Array([1, 2, 3]).buffer},
        'module.payload': {wasm: new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]).buffer},
      },
      env: {ASSETS: assets},
      globalOutbound: null,
    }));
    if (new URL(request.url).pathname === '/named') {
      return worker.getEntrypoint('Named').fetch(request);
    }
    return worker.getEntrypoint().fetch(request);
  }
}
`;

const configSource = `
using W = import "/workerd/workerd.capnp";
const config :W.Config = (
  services = [(
    name = "entry",
    worker = (
      modules = [(name = "entry.js", esModule = embed "entry.js")],
      compatibilityDate = "2026-09-28",
      compatibilityFlags = ["experimental", "enable_ctx_exports"],
      bindings = [(name = "LOADER", workerLoader = (id = "krun-v2-smoke"))]
    )
  )],
  sockets = [(
    name = "http",
    address = "127.0.0.1:${port}",
    http = (),
    service = (name = "entry")
  )]
);
`;

writeFileSync(path.join(scratch, 'entry.js'), entrySource);
writeFileSync(path.join(scratch, 'runtime.capnp'), configSource);

const child = spawn(workerd, ['serve', path.join(scratch, 'runtime.capnp'), '-I', '/', '--experimental'], {
  cwd: scratch,
  stdio: ['ignore', 'pipe', 'pipe'],
});
let output = '';
child.stdout.on('data', chunk => { output += chunk; });
child.stderr.on('data', chunk => { output += chunk; });
let exited = false;
const exitPromise = new Promise(resolve => child.once('exit', value => {
  exited = true;
  resolve(value);
}));

try {
  let response;
  for (let attempt = 0; attempt < 100; attempt++) {
    if (exited) throw new Error('workerd exited before readiness\n' + output);
    try {
      response = await fetch(`http://127.0.0.1:${port}/smoke`);
      break;
    } catch {
      await new Promise(resolve => setTimeout(resolve, 50));
    }
  }
  assert.ok(response, 'workerd did not become ready\n' + output);
  const raw = await response.text();
  assert.equal(response.status, 200, raw + '\n' + output);
  const body = JSON.parse(raw);
  assert.deepEqual(body, {
    message: 'typed-module-ok',
    legacy: 7,
    data: [1, 2, 3],
    wasm: true,
    hash: '2689367b205c16ce32ed4200942b8b8b1e262dfc70d9bc9fbc77c49699a4f1df',
    asset: 'asset:rel-smoke',
  });
  const namedResponse = await fetch(`http://127.0.0.1:${port}/named`);
  const namedRaw = await namedResponse.text();
  assert.equal(namedResponse.status, 200, namedRaw + '\n' + output);
  assert.deepEqual(JSON.parse(namedRaw), {
    named: true,
    message: 'typed-module-ok',
    legacy: 7,
    data: [1, 2, 3],
    wasm: true,
  });
  console.log('REAL WORKERD SMOKE PASSED');
} finally {
  if (!exited) {
    child.kill('SIGTERM');
    await Promise.race([
      exitPromise,
      new Promise(resolve => setTimeout(resolve, 2000)),
    ]);
  }
  rmSync(scratch, {recursive: true, force: true});
}
