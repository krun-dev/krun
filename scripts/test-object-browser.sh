#!/usr/bin/env bash
# Real browser/Go/PostgreSQL/MinIO test; never reset the application database.
set -euo pipefail
cd "$(dirname "$0")/.."

config="${1:-.data/dev.yaml}"
image="krun-e2e-minio:20250907"
mkdir -p .data
directory="$(mktemp -d "$PWD/.data/object-browser.XXXXXX")"
container="krun-object-browser-$(basename "$directory" | tr '[:upper:]' '[:lower:]')"
server_pid=""

cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill -TERM "$server_pid" 2>/dev/null || true
    wait "$server_pid" || true
  fi
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$directory"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

if ! docker image inspect "$image" >/dev/null 2>&1; then
  docker build -t "$image" -f deploy/workers-kind/Dockerfile.minio .
fi
npm run build
go build -o "$directory/server" ./tests/objectbrowser
docker run --rm -d --name "$container" \
  -p 127.0.0.1::9000 \
  -e MINIO_ROOT_USER=fixture-access \
  -e MINIO_ROOT_PASSWORD=fixture-secret-browser-only \
  "$image" server /data >/dev/null
endpoint="http://$(docker port "$container" 9000/tcp)"
for attempt in {1..100}; do
  if curl -fsS "$endpoint/minio/health/ready" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
server_args=(-config "$config" -endpoint "$endpoint")
if [[ -n "${KRUN_TEST_POSTGRES_URL:-}" ]]; then
  server_args+=(-database-url "$KRUN_TEST_POSTGRES_URL")
fi
"$directory/server" "${server_args[@]}" >"$directory/server.log" 2>&1 &
server_pid=$!
origin=""
for attempt in {1..200}; do
  origin="$(sed -n 's/^UI_ORIGIN=//p' "$directory/server.log")"
  if [[ -n "$origin" ]]; then
    break
  fi
  if ! kill -0 "$server_pid" 2>/dev/null; then
    cat "$directory/server.log" >&2
    exit 1
  fi
  sleep 0.1
done
if [[ -z "$origin" ]]; then
  echo "browser fixture server did not start" >&2
  exit 1
fi
KRUN_IAM_E2E=1 KRUN_UI_ORIGIN="$origin" npx playwright test \
  tests/ui/permissions.e2e.spec.mjs tests/ui/organization-invitations.e2e.spec.mjs \
  tests/ui/bucket-objects.spec.mjs tests/ui/r2.spec.mjs
KRUN_UI_ORIGIN="$origin" npx playwright test tests/ui/oauth.spec.mjs \
  --grep-invert 'official Wrangler'
