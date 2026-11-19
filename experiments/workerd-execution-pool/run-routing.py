"""Same-entry cross-Pod routing experiment on separate native Linux cgroups."""
import argparse
from contextlib import ExitStack
import http.client
import importlib.util
import json
from pathlib import Path
import secrets
import socket
import subprocess
import threading
import time

root = Path(__file__).resolve().parent
layout = importlib.util.spec_from_file_location("execution_bench", root / "run-linux.py")
bench = importlib.util.module_from_spec(layout)
layout.loader.exec_module(bench)


def ports(count):
    ephemeral_first, ephemeral_last = map(int, Path("/proc/sys/net/ipv4/ip_local_port_range").read_text().split())
    # workerd binds ephemeral loopback ports as the containers start.
    candidates = [port for port in range(10_000, 65_536)
                  if not ephemeral_first <= port <= ephemeral_last]
    secrets.SystemRandom().shuffle(candidates)
    reserved = []
    with ExitStack() as stack:
        for port in candidates:
            item = socket.socket()
            try:
                item.bind(("127.0.0.1", port))
            except OSError:
                item.close()
                continue
            stack.enter_context(item)
            reserved.append(port)
            if len(reserved) == count:
                return reserved
    raise RuntimeError(f"could not reserve {count} non-ephemeral loopback ports")


def get(port, path, headers=None, method="GET", body=None):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=2)
    try:
        connection.request(method, path, body=body, headers=headers or {})
        response = connection.getresponse()
        return response.status, dict(response.getheaders()), response.read().decode()
    finally:
        connection.close()


def run(args, mode):
    count = {"local-only": 1, "loop-check": 3, "routing": args.pods}[mode]
    names = [chr(65 + index) if index < 26 else f"P{index}" for index in range(count)]
    reserved = ports(3 * len(names))
    addresses = {name: reserved[index * 3:index * 3 + 3] for index, name in enumerate(names)}
    token = secrets.token_hex(32)
    groups = {}
    with ExitStack() as stack:
        for index, name in enumerate(names):
            entry, execution, metrics = addresses[name]
            peers = ",".join(
                f"{other}@127.0.0.1:{addr[1]}" for other, addr in addresses.items() if other != name
            )
            reserve = args.cpu_reserve
            if mode == "loop-check" and name in ("A", "B"):
                reserve = 3999
            env = {
                "POOL_ADMISSION": "1",
                "POOL_BENCH_POD_NAME": name,
                "POOL_EXECUTION_PORT": execution,
                "POOL_METRICS_PORT": metrics,
                "POOL_PEERS": peers,
                "POOL_PEER_TOKEN": token,
                "POOL_PEER_IN_FLIGHT": args.peer_in_flight,
                "POOL_PEER_SELECTION": args.peer_selection,
                "POOL_PEER_COOLDOWN_MS": args.cooldown_ms,
                "POOL_CPU_HIGH_MCPU": args.cpu_high,
                "POOL_CPU_RESERVE_MCPU": reserve,
                "POOL_SAMPLE_MS": args.sample_ms,
                "POOL_MEMORY_RESERVE_MIB": args.memory_reserve,
                "POOL_MAX_COLD_IN_FLIGHT": args.cold,
                "POOL_RETRY_CAPACITY_MS": 200 if mode == "loop-check" else args.retry_ms,
            }
            for key, value in (
                ("POOL_ORDINARY_IN_FLIGHT", args.ordinary_in_flight),
                ("POOL_COMPUTE_IN_FLIGHT", args.compute_in_flight),
                ("POOL_PEER_ORDINARY_IN_FLIGHT", args.peer_ordinary_in_flight),
                ("POOL_PEER_COMPUTE_IN_FLIGHT", args.peer_compute_in_flight),
            ):
                if value is not None:
                    env[key] = value
            _, _, group, _ = stack.enter_context(bench.pool_container(
                2, args.compute_processes, index, config="capacity.capnp", port=entry, extra_env=env,
                in_flight=args.in_flight,
            ))
            groups[name] = group
        time.sleep(1)

        if mode == "loop-check":
            entry_b, execution_b, metrics_b = addresses["B"]
            path = "/heap/1/0/read"
            headers = {
                "Host": "cpu-a.test",
                "x-krun-lab-target": "B",
                "x-krun-lab-hop": "1",
            }
            assert get(execution_b, path, headers)[0] == 403
            headers["x-krun-lab-peer-token"] = token
            assert get(execution_b, path, {**headers, "x-krun-lab-hop": "2"})[0] == 403
            assert get(execution_b, path, {**headers, "x-krun-lab-target": "A"})[0] == 403
            # Verify that this Pod can forward an external request with the same Host.
            for _ in range(5):
                external_headers = {key: value for key, value in headers.items() if "token" not in key}
                forwarded = get(entry_b, path, external_headers)
                if forwarded[0] == 200:
                    break
                time.sleep(0.1)
            assert forwarded[0] == 200 and forwarded[1].get("x-experiment-pod") in ("A", "C"), forwarded
            assert "x-krun-lab-execution-headroom" not in forwarded[1]
            before = get(metrics_b, "/metrics")[2]
            status, response, _ = get(execution_b, path, headers)
            assert status == 503 and response["x-krun-lab-execution-reject"] == "capacity", (status, response)
            after = get(metrics_b, "/metrics")[2]
            only_forwarded = lambda data: [line for line in data.splitlines() if 'outcome="forwarded"' in line]
            assert only_forwarded(before) == only_forwarded(after), (before, after)
            retry_metric = 'krun_execution_routes_total{execution_group="compute",outcome="retry"}'
            entry_a, _, metrics_a = addresses["A"]
            for key in range(1, 32):
                result = get(entry_a, f"/heap/1/{key}/read", {"Host": "cpu-a.test"})
                assert result[0] == 200 and result[1]["x-experiment-pod"] == "C", result
                if retry_metric + " 1" in get(metrics_a, "/metrics")[2].splitlines():
                    break
            else:
                raise AssertionError("no confirmed refusal was retried")
            for key in range(32, 64):
                time.sleep(args.cooldown_ms / 1000)
                status_post, _, _ = get(entry_a, f"/heap/1/{key}/read", {"Host": "cpu-a.test"},
                                        method="POST", body=b"benchmark")
                if status_post == 503:
                    break
            else:
                raise AssertionError("POST never received the target refusal")
            assert retry_metric + " 1" in get(metrics_a, "/metrics")[2].splitlines()
            return {"unauthenticated": 403, "hop2": 403, "wrongDestination": 403,
                    "externalForwardedTo": forwarded[1]["x-experiment-pod"], "internalRejected": status,
                    "noLoop": True, "bodylessGetRetriedTo": "C", "postRefusalNotRetried": True}

        if args.sweep_rps:
            stages = []

            rate_key = "lightRps" if args.light_load_bin else "computeRps"

            def phase(rate, seconds):
                sample = measure(args, addresses, groups, rate, seconds)
                stages.append({rate_key: rate, "seconds": seconds, **sample})
                checkpoint(args, {"routing": {"stages": stages}})
                if args.light_load_bin:
                    light = sample["light"]
                    print(json.dumps({
                        "lightRps": rate,
                        "seconds": seconds,
                        "successRps": light["succeeded"] / seconds,
                        "completedInWindowRps": light["completedInWindow"] / seconds,
                        "failures": light["failures"],
                        "p99ms": light["p99ms"],
                        "scheduledP99SlipMs": light["scheduledP99SlipMs"],
                        "activePods": len(light["byPod"]),
                        "activeSlots": len(light["bySlot"]),
                    }), flush=True)
                    return
                compute = sample["compute"]
                print(json.dumps({
                    "computeRps": rate, "seconds": seconds,
                    "computeSuccessRps": compute["succeeded"] / seconds,
                    "computeFailures": compute["attempted"] - compute["succeeded"],
                    "computeP99ms": compute["p99ms"],
                    "lightFailures": sample["light"]["attempted"] - sample["light"]["succeeded"],
                    "lightP99ms": sample["light"]["p99ms"], "activeComputePods": len(compute["byPod"]),
                }), flush=True)

            for index, rate in enumerate(args.sweep_rps):
                seconds = args.warmup_seconds if index == 0 and args.warmup_seconds else args.seconds
                phase(rate, seconds)
            if args.confirm_seconds:
                for stage in reversed(stages.copy()):
                    if all(stage[group]["succeeded"] == stage[group]["attempted"] for group in ("compute", "light")):
                        phase(stage[rate_key], args.confirm_seconds)
                        confirmation = stages[-1]
                        if all(confirmation[group]["succeeded"] == confirmation[group]["attempted"]
                               for group in ("compute", "light")):
                            break
            return {"stages": stages}

        return measure(args, addresses, groups, args.compute_rps, args.seconds)


def measure(args, addresses, groups, rate, seconds):
    baseline = {name: group.sample() for name, group in groups.items()}
    peak = {name: value["memoryBytes"] for name, value in baseline.items()}
    timeline = []
    finished = threading.Event()

    def scrape():
        samples = {}
        for name, (_, _, port) in addresses.items():
            status, _, sample = get(port, "/metrics")
            assert status == 200, (name, port, status)
            samples[name] = sample
        timeline.append({"timeMs": int(time.time() * 1000), "samples": samples})

    def observe():
        next_scrape = time.monotonic() + 2
        while not finished.wait(0.05):
            for name, group in groups.items():
                peak[name] = max(peak[name], group.sample()["memoryBytes"])
            if time.monotonic() >= next_scrape:
                scrape()
                next_scrape += 3

    scrape()
    watcher = threading.Thread(target=observe)
    watcher.start()
    try:
        if args.light_load_bin:
            ports = [str(target[0]) for target in addresses.values()] if args.spread_entries else [str(addresses["A"][0])]
            binary = str(Path(args.light_load_bin).resolve())
            raw = bench.output(binary, ",".join(ports), str(seconds), str(rate))
        else:
            raw = bench.output(
                "nerdctl", "run", "--rm", "--network", "host", *bench.MOUNT,
                "--entrypoint", "node", bench.IMAGE, "/bench/routing-load.mjs",
                str(addresses["A"][0]), str(seconds), str(args.light_rps),
                str(rate), str(args.rounds), str(args.compute_workers),
            )
    finally:
        finished.set()
        watcher.join()
    result = json.loads(raw)
    scrape()
    result["metricsTimeline"] = timeline
    result["resources"] = {}
    result["metrics"] = {}
    for name, group in groups.items():
        final = group.sample()
        result["resources"][name] = {
            "peakMemoryBytes": max(peak[name], final["memoryBytes"]),
            "cpuNs": final["cpuNs"] - baseline[name]["cpuNs"],
            "throttledPeriods": final["throttledPeriods"] - baseline[name]["throttledPeriods"],
            "memoryEvents": group.memory_events(),
        }
        status, _, sample = get(addresses[name][2], "/metrics")
        assert status == 200
        for index in range(args.compute_processes):
            assert f'krun_execution_slot_cpu_cores{{workerd_slot="compute-{index}",execution_group="compute"}}' in sample, (name, index)
        assert f"krun_execution_pod_cpu_reserved_cores {args.cpu_reserve / 1000:g}" in sample
        result["metrics"][name] = sample
    return result


def checkpoint(args, runs):
    settings = {key: value for key, value in vars(args).items() if key != "results"}
    args.results.write_text(json.dumps({"settings": settings, "runs": runs}, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--results", required=True, type=Path)
    parser.add_argument("--mode", nargs="+", choices=["local-only", "routing", "loop-check"],
                        default=["local-only", "routing", "loop-check"])
    parser.add_argument("--seconds", type=int, default=12)
    parser.add_argument("--pods", type=int, default=3)
    parser.add_argument("--sweep-rps", type=int, nargs="+")
    parser.add_argument("--confirm-seconds", type=int, default=0)
    parser.add_argument("--light-rps", type=int, default=400)
    parser.add_argument("--light-load-bin", help="native Go client for the pure-light sweep")
    parser.add_argument("--spread-entries", action="store_true", help="pin each light domain to an ingress")
    parser.add_argument("--warmup-seconds", type=int, default=0, help="override the first sweep stage duration")
    parser.add_argument("--compute-rps", type=int, default=85)
    parser.add_argument("--compute-workers", type=int, default=1)
    parser.add_argument("--compute-processes", type=int, choices=(2, 3), default=2)
    parser.add_argument("--rounds", type=int, default=22000000)
    parser.add_argument("--cpu-high", type=int, default=650)
    parser.add_argument("--cpu-reserve", type=int, default=500)
    parser.add_argument("--sample-ms", type=int, default=100)
    parser.add_argument("--peer-in-flight", type=int, default=6)
    parser.add_argument("--peer-selection", choices=("affinity", "capacity"), default="affinity")
    parser.add_argument("--cooldown-ms", type=int, default=150)
    parser.add_argument("--in-flight", type=int, default=4)
    parser.add_argument("--ordinary-in-flight", type=int)
    parser.add_argument("--compute-in-flight", type=int)
    parser.add_argument("--peer-ordinary-in-flight", type=int)
    parser.add_argument("--peer-compute-in-flight", type=int)
    parser.add_argument("--memory-reserve", type=int, default=1639)
    parser.add_argument("--cold", type=int, default=4)
    parser.add_argument("--retry-ms", type=int, default=0)
    args = parser.parse_args()
    if (not 1 <= args.pods <= 65 or args.seconds <= 0 or args.light_rps < 0
            or args.compute_rps < 0 or args.compute_workers < 1):
        parser.error("invalid Pod count, duration, request rate or Worker count")
    if args.sweep_rps and (args.mode != ["routing"] or args.sweep_rps != sorted(set(args.sweep_rps))
                           or args.sweep_rps[0] <= 0):
        parser.error("--sweep-rps requires --mode routing and strictly increasing positive rates")
    if args.light_load_bin and not args.sweep_rps:
        parser.error("--light-load-bin requires --sweep-rps")
    if args.spread_entries and not args.light_load_bin:
        parser.error("--spread-entries requires --light-load-bin")
    if args.warmup_seconds < 0 or (args.warmup_seconds and not args.sweep_rps):
        parser.error("--warmup-seconds requires a sweep")
    if args.confirm_seconds < 0 or (args.confirm_seconds and not args.sweep_rps):
        parser.error("--confirm-seconds requires a sweep")
    runs = {}
    for mode in args.mode:
        runs[mode] = run(args, mode)
        checkpoint(args, runs)
        if not args.sweep_rps:
            print(mode, json.dumps({k: v for k, v in runs[mode].items() if k not in ("metrics", "metricsTimeline")}), flush=True)


if __name__ == "__main__":
    main()
