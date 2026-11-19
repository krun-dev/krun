import { existsSync } from "node:fs";
import { Agent, request } from "node:http";
import { setTimeout as sleep } from "node:timers/promises";

const port = Number(process.argv[2]);
const chunks = [];
for await (const chunk of process.stdin) chunks.push(chunk);
const config = JSON.parse(Buffer.concat(chunks));
const agent = new Agent({ keepAlive: true, maxSockets: config.action === "burst" ? config.count : 160 });

function call(id, action, host = config.hosts[id % config.hosts.length]) {
  return new Promise(resolve => {
    const start = performance.now();
    const query = action === "cpu" && config.cpuRounds ? `?rounds=${config.cpuRounds}` : "";
    const req = request({
      hostname: "127.0.0.1", port, agent, headers: { Host: host },
      path: `/${config.storage}/${config.mib}/${id}/${action}${query}`, timeout: 15000,
    }, response => {
      const ms = performance.now() - start;
      response.resume();
      response.once("end", () => resolve({
        id, ms, status: response.statusCode, completedAt: performance.now(),
        instance: response.headers["x-experiment-instance"],
      }));
      response.once("error", error => resolve({ id, error: String(error) }));
    });
    req.once("error", error => resolve({ id, error: String(error) }));
    req.once("timeout", () => req.destroy(new Error("request timeout")));
    req.end();
  });
}

function percent(values, p) {
  values.sort((a, b) => a - b);
  return values.length ? +values[Math.ceil(p * values.length) - 1].toFixed(2) : null;
}

function report(rows, elapsedMs) {
  const ok = rows.filter(row => row.status === 200 && row.instance);
  return {
    total: rows.length, ok: ok.length,
    http503: rows.filter(row => row.status === 503).length,
    errors: rows.length - ok.length,
    p50Ms: percent(ok.map(row => row.ms), 0.50),
    p95Ms: percent(ok.map(row => row.ms), 0.95),
    p99Ms: percent(ok.map(row => row.ms), 0.99),
    elapsedMs: Math.round(elapsedMs),
  };
}

async function sweep() {
  const { from, to, instances, stopFile, action } = config;
  const start = performance.now();
  const rows = [];
  let cursor = from;
  await Promise.all(Array.from({length: 8}, async () => {
    while (cursor < to && (!stopFile || !existsSync(stopFile))) {
      // Reverse direction for the revisit to test whether earliest isolates stayed warm.
      const id = action === "revisit" ? to - 1 - cursor++ : cursor++;
      rows.push(await call(id, "read"));
    }
  }));
  const success = rows.filter(row => row.status === 200 && row.instance);
  return {
    ...report(rows, performance.now() - start),
    ...(action === "revisit"
      ? { warm: success.filter(row => instances[row.id] === row.instance).length }
      : { instances: Object.fromEntries(success.map(row => [row.id, row.instance])) }),
  };
}

async function active() {
  const { rate, count, seconds, action, probeRate = 0 } = config;
  const burst = action === "burst";
  const probeHosts = ["light-a.test", "light-b.test"];
  const ids = Array.from({length: count}, (_, id) => id);
  const isOrdinary = id => probeHosts.includes(config.hosts[id % config.hosts.length]);
  const mixed = config.hosts.includes(probeHosts[0]) && !config.hosts.every(host => probeHosts.includes(host));
  const ordinaryIds = mixed ? ids.filter(isOrdinary) : [];
  const cpuIds = mixed && action === "cpu" ? ids.filter(id => !isOrdinary(id)) : ids;
  const probeInstances = probeRate && !mixed ? await Promise.all(
    probeHosts.map(host => call(0, "read", host).then(row => row.instance)),
  ) : [];
  const warm = row => row.instance && row.instance === config.instances[row.id];
  const started = performance.now();
  const businessRequest = index => call(cpuIds[(index * 37) % cpuIds.length], action === "read" ? "read" : "cpu");
  const probeRequest = async index => {
    if (mixed) {
      const row = await call(ordinaryIds[(index * 37) % ordinaryIds.length], "read");
      return {...row, warm: warm(row)};
    }
    const slot = index % probeHosts.length;
    const row = await call(0, "read", probeHosts[slot]);
    return {...row, warm: row.instance && row.instance === probeInstances[slot]};
  };
  const runRate = async (rps, operation) => {
    const pending = [];
    const total = rps * (seconds ?? 0);
    while (pending.length < total) {
      const nextAt = pending.length * 1000 / rps;
      await sleep(Math.max(0, started + nextAt - performance.now()));
      const due = Math.min(total, Math.floor((performance.now() - started) * rps / 1000) + 1);
      while (pending.length < due) {
        const index = pending.length;
        const delay = performance.now() - started - index * 1000 / rps;
        pending.push(operation(index).then(row => ({...row, delay})));
      }
    }
    return Promise.all(pending);
  };
  const business = burst
    ? Promise.all(ids.map(index => {
      const delay = performance.now() - started;
      return businessRequest(index).then(row => ({...row, delay}));
    }))
    : runRate(rate, businessRequest);
  const [rows, probes] = await Promise.all([business, runRate(probeRate, probeRequest)]);
  const elapsed = performance.now() - started;
  const steady = values => {
    const warmupMs = 2000;
    const measuredMs = seconds * 1000 - warmupMs;
    if (measuredMs <= 0 || burst) return {};
    const success = values.filter(row => row.status === 200 && row.instance &&
      row.completedAt >= started + warmupMs && row.completedAt < started + seconds * 1000);
    return {steadyOkRps: +(success.length * 1000 / measuredMs).toFixed(2)};
  };
  return {
    ...report(rows, elapsed),
    ...steady(rows),
    dispatchDelayP99Ms: percent(rows.map(row => row.delay), 0.99),
    warm: rows.filter(warm).length,
    ...(probeRate && {ordinary: {
      requestedRps: probeRate,
      ...report(probes, elapsed),
      ...steady(probes),
      dispatchDelayP99Ms: percent(probes.map(row => row.delay), 0.99),
      warm: probes.filter(row => row.warm).length,
    }}),
  };
}

try {
  const operation = config.action === "load" || config.action === "revisit" ? sweep : active;
  console.log(JSON.stringify(await operation()));
} finally {
  agent.destroy();
}
