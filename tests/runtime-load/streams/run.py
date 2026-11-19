"""Run serving supervisor containers and inject the loss of one test Pod."""

import argparse
from contextlib import ExitStack
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent.parent


def checked(*args, **kwargs):
    subprocess.run(args, check=True, **kwargs)


def available_ports(count):
    with ExitStack() as probes:
        ports = []
        for port in range(15000, 30000):
            probe = socket.socket()
            try:
                probe.bind(("127.0.0.1", port))
            except OSError:
                probe.close()
                continue
            probes.enter_context(probe)
            ports.append(port)
            if len(ports) == count:
                return ports
    raise RuntimeError("not enough local test ports")


def metrics(port, path):
    with urllib.request.urlopen("http://127.0.0.1:{}{}".format(port, path), timeout=1) as response:
        return response.read().decode()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    parser.add_argument("--supervisor", default="supervisor")
    parser.add_argument("--workerd", default="workerd")
    parser.add_argument("--pods", type=int, default=20)
    parser.add_argument("--streams", type=int, default=10000)
    parser.add_argument("--short-rps", type=int, default=2000)
    args = parser.parse_args()
    if args.pods < 2 or args.streams < 2 or args.streams % 2 != 0 or args.short_rps < 1:
        parser.error("provide at least two Pods, an even stream count and a positive short request rate")
    prefix = "krun-stream-load-{}".format(os.getpid())
    engine = "nerdctl"
    marker = ROOT / (prefix + "-kill")
    done = Path(str(marker) + "-done")
    token = ROOT / (prefix + "-token")
    token.write_text("test-management-fixture")
    token.chmod(0o644)
    names = []
    ports = available_ports(args.pods * 4)
    node = None
    try:
        for pod in range(args.pods):
            name = "{}-{}".format(prefix, pod)
            private, management, scrape, coordinator = ports[pod*4:pod*4+4]
            env = {
                "POOL_BIND_ADDRESS": "127.0.0.1",
                "POOL_PORT": private,
                "POOL_MANAGEMENT_PORT": management,
                "POOL_METRICS_PORT": scrape,
                "POOL_COORDINATOR_PORT": coordinator,
                "POOL_MANAGEMENT_TOKEN_FILE": "/bench/" + token.name,
                "POOL_ORDINARY_IN_FLIGHT": 32,
                "POOL_CPU_HIGH_MCPU": 750,
                "POOL_CPU_RESERVE_MCPU": 1000,
                "POOL_STREAMS_ENABLED": 1,
                "POOL_SSE_PATHS": "/sse",
            }
            command = [engine, "run", "-d", "--name", name, "--network", "host",
                       "--user", "0:0", "--cpus", "4", "--memory", "8g", "-v", str(ROOT)+":/bench:ro"]
            for key, value in env.items():
                command.extend(("-e", "{}={}".format(key, value)))
            names.append(name)
            checked(*command, "--entrypoint", "/bench/" + args.supervisor, args.image,
                    "--serving", "/bench/" + args.workerd, "/bench/streams/runtime.capnp",
                    stdout=subprocess.DEVNULL)
        deadline = time.monotonic() + 40
        for port in ports[1::4]:
            while True:
                try:
                    metrics(port, "/ready")
                    break
                except (OSError, urllib.error.URLError):
                    if time.monotonic() > deadline:
                        raise
                    time.sleep(0.1)
        node = subprocess.Popen([
            engine, "run", "--rm", "--name", prefix+"-client", "--network", "host",
            "--user", "0:0", "--ulimit", "nofile=65536:65536", "-v", str(ROOT)+":/bench",
            "--entrypoint", "node", args.image, "/bench/streams/load.mjs",
            ",".join(map(str, ports[::4])), "/bench/" + marker.name, str(args.streams),
            str(args.short_rps),
        ])
        killed = False
        while node.poll() is None:
            if marker.exists() and not killed:
                checked(engine, "kill", "--signal", "KILL", names[0], stdout=subprocess.DEVNULL)
                done.touch()
                killed = True
            time.sleep(0.05)
        if node.returncode:
            raise subprocess.CalledProcessError(node.returncode, "stream load client")
        assert killed, "Pod failure was not injected"
    except BaseException:
        for name in names[:2]:
            subprocess.run((engine, "logs", "--tail", "30", name), check=False)
        raise
    finally:
        if node is not None and node.poll() is None:
            subprocess.run((engine, "rm", "-f", prefix+"-client"), check=False, stdout=subprocess.DEVNULL)
            node.wait()
        for name in names:
            subprocess.run((engine, "rm", "-f", name), check=False, stdout=subprocess.DEVNULL)
        for target in (marker, done, token):
            if target.exists():
                target.unlink()


if __name__ == "__main__":
    main()
