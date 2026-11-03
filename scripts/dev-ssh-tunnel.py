#!/usr/bin/env python3
"""Detach development SSH tunnels from the invoking terminal/tool session."""
from pathlib import Path
import subprocess
import sys

name = sys.argv[1]
if name not in ("tunnel-k8s", "tunnel-runtime") or sys.argv[2] != "ssh":
    raise SystemExit("expected a named development SSH tunnel")
root = Path(".data")
root.mkdir(exist_ok=True)
with (root / (name + ".log")).open("w") as log:
    child = subprocess.Popen(
        sys.argv[2:],
        stdin=subprocess.DEVNULL,
        stdout=log,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
pidfile = root / (name + ".pid")
pidfile.write_text(str(child.pid))
pidfile.chmod(0o600)
