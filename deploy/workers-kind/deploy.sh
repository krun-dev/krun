#!/usr/bin/env bash
set -euo pipefail
task_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$task_root"
: "${KUBECONFIG:?Set KUBECONFIG to the intended kind kubeconfig}"
: "${KIND_CLUSTER_NAME:?Set KIND_CLUSTER_NAME to the intended kind cluster}"
task_cluster="$KIND_CLUSTER_NAME"
task_node="${task_cluster}-control-plane"
task_context="$(kubectl --kubeconfig "$KUBECONFIG" config current-context)"
if [[ "$task_context" != "kind-$task_cluster" ]]; then
  echo "Expected kind-$task_cluster, got $task_context" >&2
  exit 1
fi
kubectl --kubeconfig "$KUBECONFIG" get gatewayclass kgateway >/dev/null
docker inspect "$task_node" >/dev/null
task_arch="$(kubectl --kubeconfig "$KUBECONFIG" get node "$task_node" -o jsonpath='{.status.nodeInfo.architecture}')"
case "$task_arch" in
  amd64|arm64) ;;
  *) echo "Unsupported kind node architecture: $task_arch" >&2; exit 1 ;;
esac
task_platform="linux/$task_arch"
mkdir -p deploy/workers-kind/generated
if command -v go >/dev/null; then
  CGO_ENABLED=0 GOOS=linux GOARCH="$task_arch" go build -trimpath -o deploy/workers-kind/generated/krun-server ./cmd/krun-server
elif [[ ! -x deploy/workers-kind/generated/krun-server ]]; then
  echo "Provide Go 1.26, or cross-compile an executable generated/krun-server before copying the source" >&2
  exit 1
else
  python3 - deploy/workers-kind/generated/krun-server "$task_arch" <<'PY'
import pathlib
import mmap
import struct
import sys

binary = pathlib.Path(sys.argv[1])
expected = {'amd64': 62, 'arm64': 183}[sys.argv[2]]
with binary.open('rb') as stream:
    header = stream.read(20)
if len(header) < 20 or header[:4] != b'\x7fELF':
    raise SystemExit(f'{binary} is not a Linux ELF binary; rebuild it for linux/{sys.argv[2]}')
byte_order = {1: '<', 2: '>'}.get(header[5])
if byte_order is None or struct.unpack(byte_order + 'H', header[18:20])[0] != expected:
    raise SystemExit(f'{binary} does not target linux/{sys.argv[2]}; rebuild it before deploying')
with binary.open('rb') as stream, mmap.mmap(stream.fileno(), 0, access=mmap.ACCESS_READ) as contents:
    if contents.find(b'build\tGOOS=linux\n') < 0 or contents.find(f'build\tGOARCH={sys.argv[2]}\n'.encode()) < 0:
        raise SystemExit(f'{binary} Go build metadata does not target linux/{sys.argv[2]}')
PY
fi
docker build --platform "$task_platform" -t krun-worker-builder:20261013 workers/builder
docker build --platform "$task_platform" -t krun-worker-runtime:20261013 workers/runtime
docker build --platform "$task_platform" -t krun-workers-backend:20261013 -f deploy/workers-kind/Dockerfile.backend deploy/workers-kind
docker build --platform "$task_platform" -t krun-e2e-minio:20250907 -f deploy/workers-kind/Dockerfile.minio deploy/workers-kind
for task_image in postgres:18-alpine krun-e2e-minio:20250907 krun-worker-builder:20261013 krun-worker-runtime:20261013 krun-workers-backend:20261013; do
  if [[ "$(docker image inspect --format '{{.Architecture}}' "$task_image" 2>/dev/null || true)" != "$task_arch" ]]; then
    docker pull --platform "$task_platform" "$task_image"
  fi
  docker save --platform "$task_platform" "$task_image" | docker exec -i "$task_node" ctr --namespace=k8s.io images import --platform "$task_platform" -
done
task_api_service="$(kubectl --kubeconfig "$KUBECONFIG" -n default get service kubernetes -o jsonpath='{.spec.clusterIP}')"
task_api_endpoints="$(kubectl --kubeconfig "$KUBECONFIG" -n default get endpoints kubernetes -o jsonpath='{range .subsets[*].addresses[*]}{.ip},{end}')"
export KRUN_KUBE_API_IPS="${task_api_endpoints}${task_api_service}"
python3 deploy/workers-kind/manifests.py
kubectl --kubeconfig "$KUBECONFIG" apply -f deploy/workers-kind/generated/manifests.json
# Rebuilt tags are local to this lab. Restart to consume updated images/config.
kubectl --kubeconfig "$KUBECONFIG" -n workers-mvp rollout restart deployment/backend deployment/runtime-default deployment/tester
for task_deployment in postgres minio backend runtime-default tester; do
  kubectl --kubeconfig "$KUBECONFIG" -n workers-mvp rollout status "deployment/$task_deployment" --timeout=180s
done
for task_gateway in workers-mvp workers-public; do
  kubectl --kubeconfig "$KUBECONFIG" -n kgateway-system wait --for=condition=Programmed "gateway/$task_gateway" --timeout=120s
  task_gateway_ready=false
  for _ in $(seq 1 60); do
    task_gateway_ip="$(kubectl --kubeconfig "$KUBECONFIG" -n kgateway-system get service "$task_gateway" -o jsonpath='{.spec.clusterIP}' 2>/dev/null || true)"
    task_gateway_endpoints="$(kubectl --kubeconfig "$KUBECONFIG" -n kgateway-system get endpoints "$task_gateway" -o jsonpath='{range .subsets[*].addresses[*]}{.ip}{end}' 2>/dev/null || true)"
    if [[ -n "$task_gateway_ip" && "$task_gateway_ip" != "None" && -n "$task_gateway_endpoints" ]]; then
      task_gateway_ready=true
      break
    fi
    sleep 2
  done
  if [[ "$task_gateway_ready" != true ]]; then
    echo "Gateway service $task_gateway did not receive a ClusterIP and ready endpoint" >&2
    exit 1
  fi
done
echo "Backend deployed in workers-mvp; existing workers-lab was not modified"
