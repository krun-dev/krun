"""Run isolated Linux containers with a shared workerd image and separate load client."""
import argparse
from contextlib import ExitStack, contextmanager
import http.client
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import threading
import tempfile
import time


ROOT = Path(__file__).resolve().parent
IMAGE = os.environ.get("BENCH_IMAGE", "docker.io/library/node:22-bookworm")
WORKERD = os.environ.get("BENCH_WORKERD", "/bench/workerd-krun.3")
POOL_IN_FLIGHT = int(os.environ.get("BENCH_POOL_IN_FLIGHT", "64"))
BINARY = "/bench/target/release/krun-execution-pool-experiment"
MOUNT = ["-v", str(ROOT) + ":/bench:ro"]


def output(*args):
    return subprocess.check_output(args, text=True)


def client(port, action):
    return output(
        "nerdctl", "run", "--rm", "--network", "host", *MOUNT,
        "--entrypoint", "node", IMAGE, "/bench/load.mjs", str(port), action,
    )


def request(port, host, path="/"):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=2)
    try:
        connection.request("GET", path, headers={"Host": host})
        response = connection.getresponse()
        response.read()
        return response.status, response.getheader("x-experiment-instance")
    finally:
        connection.close()


def wait_ready(port):
    end = time.monotonic() + 15
    while time.monotonic() < end:
        try:
            if request(port, "ready.test", "/__pool/ready")[0] == 200:
                return
        except OSError:
            pass
        time.sleep(0.05)
    raise RuntimeError("pool did not become ready")


class Cgroup:
    def __init__(self, pid):
        paths = {}
        for line in Path("/proc/{}/cgroup".format(pid)).read_text().splitlines():
            _, controllers, path = line.split(":", 2)
            for controller in controllers.split(","):
                paths[controller] = path.lstrip("/")
        self.v2 = "cpu" not in paths
        if self.v2:
            self.cpu = self.memory = Path("/sys/fs/cgroup") / paths[""]
        else:
            self.cpu = Path("/sys/fs/cgroup/cpu,cpuacct") / paths["cpu"]
            self.memory = Path("/sys/fs/cgroup/memory") / paths["memory"]

    def limits(self):
        if self.v2:
            quota, period = (self.cpu / "cpu.max").read_text().split()
            memory = (self.memory / "memory.max").read_text().strip()
        else:
            quota = (self.cpu / "cpu.cfs_quota_us").read_text()
            period = (self.cpu / "cpu.cfs_period_us").read_text()
            memory = (self.memory / "memory.limit_in_bytes").read_text()
        return {"cpu": int(quota) / int(period), "memoryBytes": int(memory)}

    def sample(self):
        stats = dict(line.split() for line in (self.cpu / "cpu.stat").read_text().splitlines())
        if self.v2:
            usage_ns = int(stats["usage_usec"]) * 1000
            throttled_ns = int(stats["throttled_usec"]) * 1000
            memory = int((self.memory / "memory.current").read_text())
        else:
            usage_ns = int((self.cpu / "cpuacct.usage").read_text())
            throttled_ns = int(stats["throttled_time"])
            memory = int((self.memory / "memory.usage_in_bytes").read_text())
        return {
            "cpuNs": usage_ns,
            "throttledPeriods": int(stats["nr_throttled"]),
            "periods": int(stats["nr_periods"]),
            "throttledNs": throttled_ns,
            "memoryBytes": memory,
        }

    def memory_events(self):
        if self.v2:
            events = (self.memory / "memory.events").read_text().splitlines()
        else:
            events = (self.memory / "memory.oom_control").read_text().splitlines()
            events.append("failcnt " + (self.memory / "memory.failcnt").read_text().strip())
        return {key: int(value) for key, value in map(str.split, events)}


def measure(group, operation, memory_stop=None):
    initial = group.sample()
    peak = initial["memoryBytes"]
    stopped = threading.Event()

    def sample():
        nonlocal peak
        while not stopped.is_set():
            peak = max(peak, group.sample()["memoryBytes"])
            if memory_stop and peak >= memory_stop[0]:
                memory_stop[1].touch(exist_ok=True)
            stopped.wait(0.05)

    monitor = threading.Thread(target=sample)
    monitor.start()
    try:
        result = json.loads(operation())
    finally:
        stopped.set()
        monitor.join()
    final = group.sample()
    result["cgroup"] = {
        key: final[key] - initial[key] for key in
        ("cpuNs", "throttledPeriods", "periods", "throttledNs")
    }
    result["cgroup"]["peakMemoryBytes"] = max(peak, final["memoryBytes"])
    result["cgroup"]["endMemoryBytes"] = final["memoryBytes"]
    return result


def recovery(name, port):
    logs = subprocess.check_output(("nerdctl", "logs", name), text=True, stderr=subprocess.STDOUT)
    matches = re.findall(r"start compute-0 pid=(\d+)", logs)
    assert len(matches) == 1, matches
    assert "start compute-1 " in logs

    def rank(host):
        value = 0x811c9dc5
        for byte in host.encode():
            value = ((value ^ byte) * 0x01000193) & 0xffffffff
        return value % 2

    victim = next(host for host in ("cpu-a.test", "cpu-b.test") if rank(host) == 0)
    before = request(port, victim)[1]
    ordinary = request(port, "light-a.test")[1]
    start = time.monotonic()
    output("nerdctl", "exec", name, "node", "-e",
           "process.kill(Number(process.argv[1]), 'SIGKILL')", matches[0])
    ordinary_during = request(port, "light-a.test")
    assert ordinary and ordinary_during == (200, ordinary), ordinary_during
    end = start + 8
    while time.monotonic() < end:
        logs = subprocess.check_output(("nerdctl", "logs", name), text=True, stderr=subprocess.STDOUT)
        matches = re.findall(r"start compute-0 pid=(\d+)", logs)
        if len(matches) == 2 and logs.count("ready compute-0") == 2:
            current = request(port, victim)
            assert current[0] == 200 and current[1] and current[1] != before
            assert request(port, "light-a.test") == (200, ordinary)
            assert logs.count("start compute-1 ") == 1
            return {"computeRestartMs": round((time.monotonic() - start) * 1000),
                    "ordinaryStayedWarm": True, "replacementStarted": True}
        time.sleep(0.025)
    raise AssertionError("compute process was not replaced")


@contextmanager
def pool_container(ordinary, compute, index, config="config.capnp", bench_upstream=None,
                   cpus=4, proxy_threads=2, port=None, extra_env=None, in_flight=POOL_IN_FLIGHT):
    if port is None:
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            port = probe.getsockname()[1]
    name = "krun-execution-{}-{}-{}-{}".format(os.getpid(), ordinary, compute, index)
    settings = []
    if (ordinary, compute) != (2, 2):
        settings = ["-e", "POOL_ORDINARY={}".format(ordinary),
                    "-e", "POOL_COMPUTE={}".format(compute)]
    if bench_upstream is not None:
        settings += ["-e", "POOL_BENCH_UPSTREAM=127.0.0.1:{}".format(bench_upstream)]
    extra_env = {key: str(value) for key, value in (extra_env or {}).items()}
    for key in extra_env:
        settings.extend(["-e", key])
    try:
        subprocess.check_output((
            "nerdctl", "run", "-d", "--name", name, "--network", "host",
            "--cpus", str(cpus), "--memory", "8g", *MOUNT,
            "-e", "POOL_PORT={}".format(port), "-e", "POOL_IN_FLIGHT={}".format(in_flight),
            "-e", "POOL_PROXY_THREADS={}".format(proxy_threads),
            *settings,
            "--entrypoint", BINARY, IMAGE, WORKERD, "/bench/" + config,
        ), env={**os.environ, **extra_env}, text=True)
        wait_ready(port)
        info = json.loads(output("nerdctl", "inspect", name))[0]
        group = Cgroup(info["State"]["Pid"])
        limits = group.limits()
        assert limits == {"cpu": cpus, "memoryBytes": 8 * 1024**3}, limits
        yield name, port, group, limits
    except BaseException:
        subprocess.run(("nerdctl", "logs", name), check=False)
        raise
    finally:
        subprocess.run(("nerdctl", "stop", "-t", "8", name), stdout=subprocess.DEVNULL, check=False)
        subprocess.run(("nerdctl", "rm", name), stdout=subprocess.DEVNULL, check=False)


def run(ordinary, compute, index, run_light, check_recovery, run_saturation):
    with pool_container(ordinary, compute, index) as (name, port, group, limits):
        idle = group.sample()["memoryBytes"]
        client(port, "warm")
        result = {
            "ordinary": ordinary, "compute": compute, "round": index,
            "limits": limits, "idleMemoryBytes": idle,
            "warmMemoryBytes": group.sample()["memoryBytes"],
            "smoke": json.loads(client(port, "smoke")),
        }
        if run_light:
            result["lightOnly"] = measure(group, lambda: client(port, "light"))
        result["mixed"] = measure(group, lambda: client(port, "mixed"))
        if run_saturation:
            result["allFourBusy"] = measure(group, lambda: client(port, "saturation"))
        if check_recovery:
            result["recovery"] = recovery(name, port)
        return result


def capacity_client(port, stop_dir, options):
    return subprocess.check_output([
        "nerdctl", "run", "--rm", "-i", "--network", "host", *MOUNT,
        "-v", str(stop_dir) + ":/stop:ro",
        "--entrypoint", "node", IMAGE, "/bench/capacity.mjs", str(port),
    ], input=json.dumps(options), text=True)


def proxy_throughput(args):
    binary = str(args.proxy_load_bin.resolve())
    go_env = {**os.environ, "GOMAXPROCS": "8"}
    origin = subprocess.Popen([binary, "serve", str(args.proxy_response_bytes)], stdout=subprocess.PIPE,
                              text=True, env=go_env)
    results = {"protocol": "HTTP/1.1 keep-alive", "responseBytes": args.proxy_response_bytes,
               "pingoraThreads": args.proxy_threads, "inFlightPerSlot": POOL_IN_FLIGHT, "runs": []}
    settings = {"cpus": args.proxy_cpus, "proxy_threads": args.proxy_threads}
    try:
        origin_port = int(origin.stdout.readline())
        with ExitStack() as stack:
            _, inner_port, inner, limits = stack.enter_context(
                pool_container(2, 0, "proxy-inner", bench_upstream=origin_port, **settings))
            if not args.proxy_single_hop:
                _, outer_port, outer, _ = stack.enter_context(
                    pool_container(2, 0, "proxy-outer", bench_upstream=inner_port, **settings))
            results["limitsPerProxy"] = limits
            for clients in args.proxy_connections:
                def client_at(port):
                    return subprocess.check_output([
                        binary, "load", str(port), str(args.proxy_seconds), str(clients),
                        str(args.proxy_hosts), str(args.proxy_rate),
                    ], text=True, env=go_env)

                def two_hops():
                    row = measure(inner, lambda: client_at(outer_port))
                    row["downstreamCgroup"] = row.pop("cgroup")
                    return json.dumps(row)

                stages = [("oneProxy", lambda: measure(inner, lambda: client_at(inner_port)))]
                if not args.proxy_single_hop:
                    stages = [
                        ("direct", lambda: json.loads(client_at(origin_port))),
                        *stages,
                        ("twoProxies", lambda: measure(outer, two_hops)),
                    ]
                for mode, operation in stages:
                    row = {"mode": mode, **operation()}
                    results["runs"].append(row)
                    args.results.write_text(json.dumps(results, indent=2) + "\n")
                    print(json.dumps(row), flush=True)
    finally:
        origin.terminate()
        origin.wait(timeout=5)


def capacity(args, results):
    if args.capacity_layout == "single":
        ordinary, compute, hosts = 1, 0, ["cpu-a.test", "cpu-b.test"]
    elif args.capacity_layout == "compute":
        ordinary, compute, hosts = 2, 2, ["cpu-a.test", "cpu-b.test"]
    elif args.capacity_layout == "ordinary":
        ordinary, compute, hosts = 2, 2, ["light-a.test", "light-b.test"]
    else:
        ordinary, compute = 2, 2
        hosts = ["cpu-a.test", "light-a.test", "cpu-b.test", "light-b.test"]
    with tempfile.TemporaryDirectory() as directory, pool_container(
        ordinary, compute, "capacity", "capacity.capnp"
    ) as (name, port, group, limits):
        stop_dir = Path(directory)
        stop_dir.chmod(0o755)
        stop_file = stop_dir / "memory-watermark"
        budget = limits["memoryBytes"]
        reserve = args.capacity_reserve_mib * 1024**2 if args.capacity_reserve_mib else budget // 5
        high = budget - reserve
        instances = {}
        results.update({
            "layout": args.capacity_layout, "ordinary": ordinary, "compute": compute,
            "hosts": hosts, "limits": limits, "privateMiBPerWorker": args.capacity_mib,
            "storage": args.capacity_storage,
            "cpuRounds": args.capacity_cpu_rounds,
            "poolInFlightPerProcess": POOL_IN_FLIGHT,
            "memoryWatermarkBytes": high, "memoryReserveBytes": reserve,
            "idleMemoryBytes": group.sample()["memoryBytes"],
            "stages": [],
        })

        def phase(action, memory_limit, **options):
            if stop_file.exists():
                stop_file.unlink()
            payload = {"action": action, "hosts": hosts, "mib": args.capacity_mib,
                       "storage": args.capacity_storage, "cpuRounds": args.capacity_cpu_rounds, **options}
            if memory_limit:
                payload["stopFile"] = "/stop/memory-watermark"
            result = measure(group, lambda: capacity_client(port, stop_dir, payload),
                             (memory_limit, stop_file) if memory_limit else None)
            if memory_limit:
                result["memoryStop"] = stop_file.exists()
            return result

        def checkpoint():
            args.results.write_text(json.dumps(results, indent=2) + "\n")
            print(json.dumps(results["stages"][-1]), flush=True)

        def diagnostics():
            logs = subprocess.check_output(("nerdctl", "logs", name), text=True, stderr=subprocess.STDOUT)
            return {
                "memoryEvents": group.memory_events(),
                "workerdStarts": len(re.findall(r"^start (ordinary|compute)-\d+ pid=", logs, re.M)),
                "workerdErrors": [line for line in logs.splitlines()
                                  if re.search(r"fatal|out of memory|exited:|unable to allocate", line, re.I)][:16],
            }

        for target in args.capacity_counts:
            previous = len(instances)
            cold = phase("load", high, **{"from": previous, "to": target})
            instances.update(cold.pop("instances"))
            count = len(instances)
            stage = {"target": target, "loaded": count, "load": cold}
            results["stages"].append(stage)
            if cold["errors"] or len(set(instances.values())) != count:
                results["diagnostics"] = diagnostics()
                print(json.dumps(results["diagnostics"]), flush=True)
                checkpoint()
                break
            stage["revisit"] = phase("revisit", limits["memoryBytes"] * 9 // 10,
                                     **{"from": 0, "to": count, "instances": instances})
            checkpoint()
            if count == 2000 and args.capacity_idle_seconds:
                time.sleep(args.capacity_idle_seconds)
                stage["afterIdle"] = phase("revisit", limits["memoryBytes"] * 9 // 10,
                                            **{"from": 0, "to": count, "instances": instances})
                stage["idleSeconds"] = args.capacity_idle_seconds
                checkpoint()
            if count == args.capacity_active_count and args.capacity_active:
                assert stage["revisit"]["warm"] == count, stage["revisit"]
                stage["active"] = []
                default_cpu_rates = (10, 20, 30, 50) if args.capacity_probe_rps else (10, 20, 50)
                if args.capacity_layout == "ordinary":
                    default_cpu_rates = ()
                cpu_rates = args.capacity_cpu_rates if args.capacity_cpu_rates is not None else default_cpu_rates
                for action, rates in [("read", args.capacity_read_rates), ("cpu", cpu_rates)]:
                    for rate in rates:
                        probes = args.capacity_probe_rps if action == "cpu" else 0
                        active = phase(action, None, rate=rate, count=count,
                                       seconds=args.capacity_active_seconds, probeRate=probes,
                                       instances=instances)
                        stage["active"].append({"action": action, "requestedRps": rate, **active})
                        args.results.write_text(json.dumps(results, indent=2) + "\n")
                        print(json.dumps(stage["active"][-1]), flush=True)
            if count == 2000 and args.capacity_burst:
                stage["cpuBurst"] = phase("burst", None, count=count, instances=instances)
                checkpoint()
            if cold["memoryStop"] or stage["revisit"]["memoryStop"]:
                break
        if "diagnostics" not in results:
            results["diagnostics"] = diagnostics()
        args.results.write_text(json.dumps(results, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--rounds", default=3, type=int)
    parser.add_argument("--results", required=True, type=Path)
    parser.add_argument("--saturation-only", action="store_true")
    parser.add_argument("--proxy-load-bin", type=Path)
    parser.add_argument("--proxy-seconds", type=int, default=6)
    parser.add_argument("--proxy-connections", type=int, nargs="+", default=(16, 32, 64, 128))
    parser.add_argument("--proxy-response-bytes", type=int, default=128)
    parser.add_argument("--proxy-hosts", type=int, default=256)
    parser.add_argument("--proxy-rate", type=int, default=0)
    parser.add_argument("--proxy-cpus", type=int, default=4)
    parser.add_argument("--proxy-threads", type=int, default=2)
    parser.add_argument("--proxy-single-hop", action="store_true")
    parser.add_argument("--capacity-layout", choices=("single", "compute", "ordinary", "mixed"))
    parser.add_argument("--capacity-mib", type=int, choices=(0, 1, 3), default=0)
    parser.add_argument("--capacity-storage", choices=("buffer", "heap"), default="buffer")
    parser.add_argument("--capacity-reserve-mib", type=int, choices=(0, 1024), default=0,
                        help="0 reserves 20%%; 1024 reserves a fixed 1 GiB")
    parser.add_argument("--capacity-counts", type=int, nargs="+", default=(500, 1000, 2000, 4000))
    parser.add_argument("--capacity-active", action="store_true")
    parser.add_argument("--capacity-active-count", type=int, default=2000)
    parser.add_argument("--capacity-active-seconds", type=int, default=8)
    parser.add_argument("--capacity-read-rates", type=int, nargs="*", default=(250,))
    parser.add_argument("--capacity-cpu-rates", type=int, nargs="*")
    parser.add_argument("--capacity-cpu-rounds", type=int, default=36000000)
    parser.add_argument("--capacity-probe-rps", type=int, default=0)
    parser.add_argument("--capacity-burst", action="store_true")
    parser.add_argument("--capacity-idle-seconds", type=int, default=0)
    args = parser.parse_args()
    if args.proxy_load_bin:
        if min(args.proxy_seconds, min(args.proxy_connections), args.proxy_response_bytes, args.proxy_hosts) < 1:
            parser.error("proxy duration, connections, response size and host count must be positive")
        if args.proxy_rate < 0:
            parser.error("proxy rate must be nonnegative")
        if args.proxy_cpus < 1 or not 1 <= args.proxy_threads <= 64:
            parser.error("proxy CPU quota must be positive and threads must be between 1 and 64")
        proxy_throughput(args)
        return
    results = {"image": IMAGE, "workerd": WORKERD, "runs": []}
    if args.capacity_layout:
        if list(args.capacity_counts) != sorted(set(args.capacity_counts)) or args.capacity_counts[0] < 1:
            parser.error("capacity counts must be positive and strictly increasing")
        if args.capacity_idle_seconds < 0:
            parser.error("capacity idle seconds must be nonnegative")
        if args.capacity_active_seconds < 1 or args.capacity_probe_rps < 0:
            parser.error("active test duration must be positive and probe RPS nonnegative")
        if args.capacity_active and args.capacity_active_count not in args.capacity_counts:
            parser.error("active test count must be one of the capacity counts")
        if any(rate <= 0 for rate in (*args.capacity_read_rates, *(args.capacity_cpu_rates or ()))):
            parser.error("active test rates must be positive")
        if not 1 <= args.capacity_cpu_rounds <= 36000000:
            parser.error("CPU round count must be between 1 and 36000000")
        results.pop("runs")
        capacity(args, results)
        return
    variants = [(2, 2)] if args.saturation_only else [(1, 0), (2, 1), (2, 2)]
    for i in range(args.rounds):
        for position in range(len(variants)):
            ordinary, compute = variants[(i + position) % len(variants)]
            result = run(ordinary, compute, i + 1, i == 0,
                         i == 0 and compute == 2, args.saturation_only)
            results["runs"].append(result)
            args.results.write_text(json.dumps(results, indent=2) + "\n")
            print("round {} {}+{} {}".format(i + 1, ordinary, compute, result["mixed"]), flush=True)


if __name__ == "__main__":
    main()
