import assert from "node:assert/strict";
import { Agent, request } from "node:http";
import { setTimeout as sleep } from "node:timers/promises";

const port = Number(process.argv[2]);
const action = process.argv[3];
const agent = new Agent({keepAlive: true, maxSockets: 192});
const lightHosts = ["light-a.test", "light-b.test"];
const cpuHosts = ["cpu-a.test", "cpu-b.test"];

function call(host, path = "/", options = {}) {
  return new Promise((resolve, reject) => {
    const start = performance.now();
    let firstChunkMs;
    let chunks = [];
    const req = request({
      hostname: "127.0.0.1",
      port,
      path,
      headers: {Host: host},
      agent,
      method: options.body ? "POST" : "GET",
      timeout: 8000,
    }, response => {
      const headersMs = performance.now() - start;
      response.on("data", chunk => {
        firstChunkMs ??= performance.now() - start;
        chunks.push(chunk);
      });
      response.on("end", () => resolve({
        status: response.statusCode,
        id: response.headers["x-experiment-instance"],
        headersMs,
        firstChunkMs,
        endMs: performance.now() - start,
        body: Buffer.concat(chunks).toString(),
      }));
      response.on("error", reject);
    });
    req.on("error", reject);
    req.on("timeout", () => req.destroy(new Error("request timeout")));
    req.end(options.body);
  });
}

async function warm() {
  for (const host of cpuHosts) {
    for (let i = 0; i < 5; i++) assert.equal((await call(host, "/cpu")).status, 200);
  }
  for (const host of lightHosts) assert.equal((await call(host)).status, 200);
}

async function smoke() {
  for (const host of [...lightHosts, ...cpuHosts]) {
    const first = await call(host);
    assert.equal(first.status, 200);
    assert.ok(first.id);
    assert.equal((await call(host)).id, first.id);
  }
  const posted = "payload ".repeat(8192);
  const echo = await call(lightHosts[0], "/echo", {body: posted});
  assert.equal(echo.status, 200);
  assert.equal(echo.body, posted);
  const stream = await call(lightHosts[0], "/stream");
  assert.equal(stream.body, "firstsecond");
  assert.ok(stream.endMs - stream.firstChunkMs >= 50, "response stream buffered");
  const waiting = await Promise.all(
    Array.from({length: 8}, () => call(lightHosts[0], "/wait")),
  );
  assert.ok(waiting.every(sample => sample.status === 200));
  return {echoBytes: posted.length, streamGapMs: stream.endMs - stream.firstChunkMs};
}

function percent(rows, key, p) {
  if (rows.length === 0) return null;
  const sorted = rows.map(row => row[key]).sort((a, b) => a - b);
  return +sorted[Math.ceil(p * sorted.length) - 1].toFixed(2);
}

function report(rows) {
  const ok = rows.filter(row => row.status === 200);
  const loaded = ok.filter(row => row.id);
  return {
    total: rows.length, ok: ok.length,
    http503: rows.filter(row => row.status === 503).length,
    failures: rows.filter(row => row.error).length,
    p50: percent(ok, "headersMs", 0.50),
    p95: percent(ok, "headersMs", 0.95),
    p99: percent(ok, "headersMs", 0.99),
    versionsPerHost: Object.fromEntries(
      [...new Set(loaded.map(row => row.host))]
        .map(host => [host, new Set(loaded.filter(row => row.host === host).map(row => row.id)).size]),
    ),
  };
}

async function load(mixed) {
  const durationMs = 9000;
  const planned = [];
  for (let t = 0; t < durationMs; t += 10) {
    planned.push([t, lightHosts[(t / 10) % lightHosts.length], "/"]);
  }
  if (mixed) {
    for (let t = 200; t < durationMs - 300; t += 450) {
      for (const host of cpuHosts) {
        for (let i = 0; i < 3; i++) planned.push([t, host, "/cpu"]);
      }
    }
  }
  const begin = performance.now();
  const results = await Promise.all(planned.map(async ([at, host, path]) => {
    await sleep(at);
    const dispatchDelayMs = performance.now() - begin - at;
    try {
      return {...await call(host, path), dispatchDelayMs, host, path};
    } catch (error) {
      return {host, path, dispatchDelayMs, error: String(error)};
    }
  }));
  return {
    elapsedMs: Math.round(performance.now() - begin),
    generatorDelayP99: percent(results, "dispatchDelayMs", 0.99),
    light: report(results.filter(row => row.path === "/")),
    compute: report(results.filter(row => row.path === "/cpu")),
  };
}

async function saturation() {
  const jobs = [];
  const started = performance.now();
  const end = started + 6000;
  const work = [...lightHosts, ...cpuHosts].map(async host => {
    while (performance.now() < end) {
      try {
        jobs.push({...await call(host, "/cpu"), host});
      } catch (error) {
        jobs.push({host, error: String(error)});
      }
    }
  });
  const [probes] = await Promise.all([
    Promise.all(Array.from({length: 600}, async (_, index) => {
      const at = index * 10;
      await sleep(at);
      const dispatchDelayMs = performance.now() - started - at;
      try {
        return {...await call("ready.test", "/__pool/ready"), dispatchDelayMs};
      } catch (error) {
        return {dispatchDelayMs, error: String(error)};
      }
    })),
    Promise.all(work),
  ]);
  return {
    elapsedMs: Math.round(performance.now() - started),
    generatorDelayP99: percent(probes, "dispatchDelayMs", 0.99),
    router: report(probes),
    compute: report(jobs),
  };
}

try {
  if (action === "warm") await warm();
  else if (action === "smoke") console.log(JSON.stringify(await smoke()));
  else if (action === "light" || action === "mixed") console.log(JSON.stringify(await load(action === "mixed")));
  else if (action === "saturation") console.log(JSON.stringify(await saturation()));
  else throw new Error("unknown action");
} finally {
  agent.destroy();
}
