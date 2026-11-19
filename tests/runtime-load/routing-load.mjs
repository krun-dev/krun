import http from "node:http";
import { performance } from "node:perf_hooks";
import { setTimeout as delay } from "node:timers/promises";

const [port, duration, lightRate, computeRate, rounds, computeWorkers = 1] = process.argv.slice(2).map(Number);
const agent = new http.Agent({ keepAlive: true, maxSockets: 512, maxFreeSockets: 64 });

async function get(host, path, workerId) {
  const start = performance.now();
  return new Promise((resolve) => {
    const req = http.get({
      hostname: "127.0.0.1", port, path, agent, headers: {Host: host},
    }, (res) => {
      res.on("error", (error) => resolve({error: error.code || error.message, workerId}));
      res.on("end", () => resolve({
        ms: performance.now() - start,
        status: res.statusCode,
        pod: res.headers["x-experiment-pod"],
        slot: res.headers["x-experiment-workerd-slot"],
        cold: res.headers["x-experiment-cold"] === "1",
        workerId,
      }));
      res.resume();
    });
    req.setTimeout(3000, () => req.destroy(new Error("timeout")));
    req.on("error", (error) => resolve({error: error.code || error.message, workerId}));
  });
}

function load(rate, start, make) {
  const attempts = [];
  const slips = [];
  return (async () => {
    for (let i = 0; i < rate * duration;) {
      const elapsed = performance.now() - start;
      const next = i * 1000 / rate;
      if (elapsed < next) {
        await delay(Math.max(1, Math.ceil(next - elapsed)));
        continue;
      }
      slips.push(elapsed - next);
      attempts.push(get(...make(i)));
      i++;
    }
    return { attempts: await Promise.all(attempts), slips };
  })();
}

function percentile(sorted, fraction) {
  return sorted.length ? sorted[Math.ceil(sorted.length * fraction) - 1] : null;
}

function summarize({attempts, slips}) {
  const failures = {};
  const byPod = {};
  const bySlot = {};
  let cold = 0;
  const times = [];
  for (const attempt of attempts) {
    if (attempt.status !== 200 || !attempt.pod || !attempt.slot) {
      const reason = attempt.error || (attempt.status === 200 ? "missing route headers" : attempt.status);
      failures[reason] = (failures[reason] || 0) + 1;
      continue;
    }
    times.push(attempt.ms);
    byPod[attempt.pod] = (byPod[attempt.pod] || 0) + 1;
    const slot = `${attempt.pod}/${attempt.slot}`;
    bySlot[slot] = (bySlot[slot] || 0) + 1;
    if (attempt.cold) cold++;
  }
  times.sort((a, b) => a - b);
  slips.sort((a, b) => a - b);
  return {
    attempted: attempts.length, succeeded: times.length, cold, byPod, bySlot, failures,
    p50ms: percentile(times, 0.50), p99ms: percentile(times, 0.99),
    scheduledP99SlipMs: percentile(slips, 0.99),
  };
}

const light = (i) => [`light-${i % 64}.test`, `/heap/1/${i % 64}/read`];
for (let i = 0; i < 64; i++) {
  const warm = await get(...light(i));
  if (warm.status !== 200) throw new Error(`warmup failed ${JSON.stringify(warm)}`);
}
for (let id = 0; id < computeWorkers; id++) {
  const initial = await get("cpu-a.test", `/heap/1/${id}/read`);
  if (initial.status !== 200) throw new Error(`compute warmup failed for Worker ${id}`);
}
const start = performance.now() + 50;
const results = await Promise.all([
  load(lightRate, start, light),
  load(computeRate, start, (i) => {
    const id = i % computeWorkers;
    return ["cpu-a.test", `/heap/1/${id}/cpu?rounds=${rounds}&loadDelayMs=25`, id];
  }),
]);
agent.destroy();
const compute = summarize(results[1]);
if (computeWorkers > 1) {
  const attemptsByWorker = Array.from({length: computeWorkers}, () => []);
  for (const attempt of results[1].attempts) {
    attemptsByWorker[attempt.workerId].push(attempt);
  }
  compute.byWorker = attemptsByWorker.map((attempts, id) => ({
    id, ...summarize({attempts, slips: []}),
  }));
}
console.log(JSON.stringify({light: summarize(results[0]), compute}));
