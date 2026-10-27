#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -f .data/dev.env ]]; then
  while IFS='=' read -r key value; do
    [[ -z "$key" || "$key" == \#* ]] && continue
    [[ -n "${!key:-}" ]] && continue
    export "$key=$value"
  done <.data/dev.env
fi

# Single gitignored local dev config: control plane + Postgres + MinIO + builder
# run here; only the runtime (workerd) and kgateway stay in the cluster.
task_config="${1:-.data/dev.yaml}"
exec ./bin/krun-server serve --config "$task_config"
