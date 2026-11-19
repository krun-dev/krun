import assert from "node:assert/strict";
import { existsSync, writeFileSync } from "node:fs";
import http from "node:http";
import { setTimeout as sleep } from "node:timers/promises";

const [ports, marker, rawCount, rawRps] = process.argv.slice(2);
const targets = ports.split(",").map(Number);
const streams = Number(rawCount);
const shortRps = Number(rawRps);
const live = new Map();
const closes = new Set();
const agent = new http.Agent({ keepAlive: true, maxSockets: 128 });
const measurements = {};
let phase = "baseline";
let running = true;
let failedPod = false;
let reconnects = 0;
const active = { sse: 0, websocket: 0 };
let messages = { sse: 0, websocket: 0 };

function metrics(section) {
  return (measurements[section] ??= { short: [], sse: [], websocket: [], failed: {} });
}

function failure(section, kind, reason) {
  const group = metrics(section).failed;
  const key = kind + ":" + reason;
  group[key] = (group[key] ?? 0) + 1;
}

function p99(values) {
  if (!values.length) return null;
  const sorted = [...values].sort((a, b) => a - b);
  return Math.round(sorted[Math.ceil(sorted.length * 0.99) - 1] * 100) / 100;
}

function summary(section) {
  const group = metrics(section);
  return {
    shortOK: group.short.length,
    shortP99Ms: p99(group.short),
    sseConnected: group.sse.length,
    sseFirstEventP99Ms: p99(group.sse),
    websocketConnected: group.websocket.length,
    websocketHandshakeP99Ms: p99(group.websocket),
    failures: group.failed,
  };
}

function sse(port, opened) {
  return new Promise((resolve) => {
    const req = http.get({ port, host: "127.0.0.1", path: "/sse", agent: false }, (res) => {
      if (res.statusCode !== 200) {
        res.resume();
        resolve(res.statusCode);
        return;
      }
      let buffered = "";
      res.setEncoding("utf8");
      res.on("data", (chunk) => {
        buffered += chunk;
        let split;
        while ((split = buffered.indexOf("\n\n")) >= 0) {
          buffered = buffered.slice(split + 2);
          opened();
          messages.sse++;
        }
      });
      res.on("error", (err) => resolve(err.code));
      res.on("close", () => resolve("closed"));
    });
    const close = () => req.destroy();
    req.once("close", () => closes.delete(close));
    req.once("error", (err) => resolve(err.code));
    req.setTimeout(5000, close);
    closes.add(close);
  });
}

function websocket(port, opened) {
  return new Promise((resolve) => {
    const socket = new WebSocket(`ws://127.0.0.1:${port}/ws`);
    const close = () => socket.close();
    const deadline = setTimeout(close, 5000);
    let interval;
    closes.add(close);
    socket.addEventListener("open", () => {
      clearTimeout(deadline);
      opened();
      interval = setInterval(() => {
        if (socket.readyState === WebSocket.OPEN) socket.send("ping");
      }, 1000);
    });
    socket.addEventListener("message", () => messages.websocket++);
    socket.addEventListener("close", () => {
      clearTimeout(deadline);
      clearInterval(interval);
      closes.delete(close);
      resolve("closed");
    });
  });
}

async function hold(index) {
  const isSse = index % 2 === 0;
  const kind = isSse ? "sse" : "websocket";
  let attempt = 0;
  while (running) {
    const eligible = failedPod ? targets.slice(1) : targets;
    const port = eligible[(Math.floor(index / 2) + attempt++) % eligible.length];
    const section = phase;
    const start = performance.now();
    let opened = false;
    const reason = await (isSse ? sse : websocket)(port, () => {
      if (opened) return;
      opened = true;
      if (phase === "failure") reconnects++;
      active[kind]++;
      live.set(index, port);
      metrics(section)[kind].push(performance.now() - start);
    });
    if (opened) {
      live.delete(index);
      active[kind]--;
    }
    if (running) failure(phase, kind, reason);
    await sleep(30 + (index % 20) * 7);
  }
}

let sent = 0;
const begin = performance.now();
const short = setInterval(() => {
  const expected = Math.round(((performance.now() - begin) * shortRps) / 1000);
  if (phase === "failure" && !failedPod) failedPod = existsSync(marker + "-done");
  const batch = Math.min(expected - sent, Math.max(1, Math.ceil(shortRps / 100)));
  for (let i = 0; i < batch; i++) {
    const index = sent++;
    const section = phase;
    const group = index % targets.length;
    const port =
      failedPod && group === 0
        ? targets[1 + (Math.floor(index / targets.length) % (targets.length - 1))]
        : targets[group];
    const start = performance.now();
    const req = http.get(
      {
        port,
        host: "127.0.0.1",
        path: "/short",
        headers: { host: "short-" + group + ".test" },
        agent,
      },
      (res) => {
        res.resume().once("end", () => {
          if (res.statusCode === 200) metrics(section).short.push(performance.now() - start);
          else failure(section, "short", res.statusCode);
        });
      },
    );
    req.once("error", (err) => failure(section, "short", err.code));
    req.setTimeout(1000, () => req.destroy());
  }
}, 5);

async function untilConnected(timeout) {
  const deadline = performance.now() + timeout;
  while (live.size < streams && performance.now() < deadline) await sleep(50);
  assert.equal(live.size, streams, JSON.stringify({ ...active, failures: metrics(phase).failed }));
}

try {
  await sleep(8000);
  phase = "ramp";
  for (let index = 0; index < streams; index++) {
    void hold(index);
    if (index % targets.length === targets.length - 1) await sleep(20);
  }
  await untilConnected(30000);
  phase = "steady";
  messages = { sse: 0, websocket: 0 };
  await sleep(12000);
  assert.equal(live.size, streams);
  console.log(JSON.stringify({ phase, active, messages, ...summary(phase) }));
  phase = "failure";
  const affected = [...live.values()].filter((port) => port === targets[0]).length;
  assert.ok(affected > 0);
  const failedAt = performance.now();
  writeFileSync(marker, "kill\n");
  for (let samples = 0; (!failedPod || reconnects < affected) && samples < 600; samples++) {
    await sleep(50);
  }
  await untilConnected(30000);
  assert.ok(failedPod && reconnects >= affected);
  console.log(
    JSON.stringify({
      phase,
      active,
      affected,
      reconnects,
      recoveredMs: Math.round(performance.now() - failedAt),
      ...summary(phase),
    }),
  );
  phase = "recovered";
  messages = { sse: 0, websocket: 0 };
  await sleep(8000);
  assert.equal(live.size, streams);
  console.log(JSON.stringify({ phase, active, messages, ...summary(phase) }));
  console.log(JSON.stringify({ phase: "baseline", ...summary("baseline") }));
  console.log(JSON.stringify({ phase: "ramp", ...summary("ramp") }));
} finally {
  running = false;
  clearInterval(short);
  agent.destroy();
  for (const close of closes) close();
}
