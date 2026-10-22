#!/usr/bin/env bash
# Stop the local dev processes started by scripts/dev-up.sh.
# The remote MinIO and the cluster runtime are intentionally left running.
set -euo pipefail
cd "$(dirname "$0")/.."

for name in krun-server-dev vite-dev tunnel-runtime tunnel-k8s; do
  pidfile=".data/$name.pid"
  if [[ -f "$pidfile" ]]; then
    pid="$(cat "$pidfile")"
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
    rm -f "$pidfile"
  fi
done
echo "local dev services stopped; remote MinIO and runtime are still running"
