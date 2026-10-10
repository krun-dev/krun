#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

pg_bin="${KRUN_PG_BIN:-}"
if [[ -z "$pg_bin" ]]; then
  if [[ -x .data/pg-bin/bin/pg_ctl ]]; then
    pg_bin="$PWD/.data/pg-bin/bin"
  else
    pg_bin="$(dirname "$(command -v pg_ctl)")"
  fi
fi
pg_data="${KRUN_PG_DATA:-$PWD/.data/pg-data}"
pg_port="${KRUN_PG_PORT:-25432}"
mkdir -p .data/pg-runtime

if [[ ! -f "$pg_data/PG_VERSION" ]]; then
  "$pg_bin/initdb" -D "$pg_data" -U krun -A scram-sha-256 -W
fi
if ! "$pg_bin/pg_ctl" -D "$pg_data" status >/dev/null 2>&1; then
  "$pg_bin/pg_ctl" -D "$pg_data" -l "$PWD/.data/pg-runtime/postgres.log" \
    -o "-h 127.0.0.1 -p $pg_port -k $PWD/.data/pg-runtime" start
fi
if [[ "${KRUN_LANG:-zh-CN}" == en* ]]; then
  printf 'PostgreSQL ready on 127.0.0.1:%s\n' "$pg_port"
else
  printf 'PostgreSQL 已就绪：127.0.0.1:%s\n' "$pg_port"
fi
