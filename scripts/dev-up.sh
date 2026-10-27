#!/usr/bin/env bash
# Single local-first development environment.
# Development machine: control plane + Postgres + MinIO + builder + Vite.
# Remote host: nginx and kind with runtime + kgateway only.
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -f .data/dev.env ]]; then
  while IFS='=' read -r key value; do
    [[ -z "$key" || "$key" == \#* ]] && continue
    [[ -n "${!key:-}" ]] && continue
    export "$key=$value"
  done <.data/dev.env
fi
: "${KRUN_DEV_REMOTE:?set KRUN_DEV_REMOTE in .data/dev.env}"
REMOTE="$KRUN_DEV_REMOTE"
KUBECONFIG_REMOTE="${KRUN_DEV_KUBECONFIG:-~/kind/kubeconfig}"
mkdir -p .data

# 1. MinIO is deliberately an independently managed local process. Starting it
# requires credentials from .data/dev.yaml, so this script only verifies the
# configured local endpoint and never duplicates those secrets in dev.env.
MINIO_LOCAL_URL="${KRUN_DEV_MINIO_LOCAL_URL:-http://127.0.0.1:9000}"
if ! curl -fsS -o /dev/null --max-time 2 "$MINIO_LOCAL_URL/minio/health/ready"; then
  echo "local MinIO is not ready at $MINIO_LOCAL_URL; start it before dev-up" >&2
  exit 1
fi

# 2. Tunnels: Kubernetes API, runtime management, and gateways.
if ! lsof -nP -iTCP:38443 -sTCP:LISTEN >/dev/null 2>&1; then
  nohup ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 \
    -L 127.0.0.1:38443:127.0.0.1:38443 "$REMOTE" >.data/tunnel-k8s.log 2>&1 &
  echo $! >.data/tunnel-k8s.pid
fi
runtime_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:38083/ready 2>/dev/null || true)
private_gateway_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:38080/ 2>/dev/null || true)
public_gateway_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:38081/ 2>/dev/null || true)
if [[ "$runtime_code" != "200" && "$runtime_code" != "503" ]] || [[ "$private_gateway_code" == "000" || "$public_gateway_code" == "000" ]]; then
  # A runtime rollout replaces the pod behind the port-forward; the listening
  # SSH socket can survive while the remote channel is dead. Probe /ready and
  # restart the tunnel on connection failure (000), not on a 503 readiness.
  # Probe both Gateway channels too: the SSH listener can outlive either
  # remote kubectl port-forward process.
  for port in 38083 38080 38081; do
    for pid in $(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true); do
      kill "$pid" 2>/dev/null || true
    done
  done
  # A disconnected SSH session can leave its remote kubectl children alive.
  # Clear only these three development port-forwards before binding the same
  # remote ports again.
  ssh -o ConnectTimeout=8 "$REMOTE" \
    "pkill -f '[p]ort-forward.*38083:8083' 2>/dev/null || true; \
     pkill -f '[p]ort-forward.*38080:8080' 2>/dev/null || true; \
     pkill -f '[p]ort-forward.*38081:8080' 2>/dev/null || true"
  # 38080 fronts the private Gateway instance, 38081 the public one. Console
  # invoke picks the target by the environment's network scope.
  nohup ssh -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 \
    -L 127.0.0.1:38083:127.0.0.1:38083 \
    -L 127.0.0.1:38080:127.0.0.1:38080 \
    -L 127.0.0.1:38081:127.0.0.1:38081 "$REMOTE" \
    "kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp port-forward deployment/runtime-default 38083:8083 --address 127.0.0.1 & kubectl --kubeconfig $KUBECONFIG_REMOTE -n kgateway-system port-forward svc/workers-mvp 38080:8080 --address 127.0.0.1 & kubectl --kubeconfig $KUBECONFIG_REMOTE -n kgateway-system port-forward deploy/workers-public 38081:8080 --address 127.0.0.1 & wait" \
    >.data/tunnel-runtime.log 2>&1 &
  tunnel_pid=$!
  echo "$tunnel_pid" >.data/tunnel-runtime.pid

  tunnel_ready=false
  for _ in $(seq 1 20); do
    runtime_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:38083/ready 2>/dev/null || true)
    private_gateway_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:38080/ 2>/dev/null || true)
    public_gateway_code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:38081/ 2>/dev/null || true)
    if [[ ("$runtime_code" == "200" || "$runtime_code" == "503") && "$private_gateway_code" != "000" && "$public_gateway_code" != "000" ]]; then
      tunnel_ready=true
      break
    fi
    if ! kill -0 "$tunnel_pid" 2>/dev/null; then
      break
    fi
    sleep 0.5
  done
  if [[ "$tunnel_ready" != true ]]; then
    echo "runtime/Gateway tunnels failed; check .data/tunnel-runtime.log" >&2
    exit 1
  fi
fi

# 3. Control plane binary. Backend/builder code changes only need a restart;
# no images are built for either.
go build -trimpath -o bin/krun-server ./cmd/krun-server
if ! lsof -nP -iTCP:8080 -sTCP:LISTEN >/dev/null 2>&1; then
  nohup ./scripts/dev-server.sh >.data/krun-server-dev.log 2>&1 &
  echo $! >.data/krun-server-dev.pid
fi

# 4. Frontend.
if ! lsof -nP -iTCP:5173 -sTCP:LISTEN >/dev/null 2>&1; then
  nohup npm run dev >.data/vite-dev.log 2>&1 &
  echo $! >.data/vite-dev.pid
fi

for i in $(seq 1 30); do
  if curl -fsS -o /dev/null http://127.0.0.1:8080/api/healthz 2>/dev/null && curl -fsS -o /dev/null http://127.0.0.1:5173/ 2>/dev/null; then
    echo "console: http://127.0.0.1:5173/"
    echo "api:     http://127.0.0.1:8080/api/"
    exit 0
  fi
  sleep 1
done
echo "services did not become healthy; check .data/krun-server-dev.log and .data/vite-dev.log"
exit 1
