#!/usr/bin/env bash
set -euo pipefail
task_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$task_root"
: "${KUBECONFIG:?Set KUBECONFIG to the intended kind kubeconfig}"
task_cluster="${KIND_CLUSTER_NAME:-krun-workers-20261010}"
task_node="${task_cluster}-control-plane"
task_context="$(kubectl --kubeconfig "$KUBECONFIG" config current-context)"
if [[ "$task_context" != "kind-$task_cluster" ]]; then
  echo "Expected kind-$task_cluster, got $task_context" >&2
  exit 1
fi
kubectl --kubeconfig "$KUBECONFIG" get gatewayclass kgateway >/dev/null
docker inspect "$task_node" >/dev/null
mkdir -p deploy/workers-kind/generated
if command -v go >/dev/null; then
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o deploy/workers-kind/generated/krun-server ./cmd/krun-server
elif [[ ! -f deploy/workers-kind/generated/krun-server ]]; then
  echo "Provide Go 1.26, or cross-compile generated/krun-server before copying the source" >&2
  exit 1
fi
docker build --platform linux/amd64 -t krun-worker-builder:20261013 workers/builder
docker build --platform linux/amd64 -t krun-worker-runtime:20261013 workers/runtime
docker build --platform linux/amd64 -t krun-workers-backend:20261013 -f deploy/workers-kind/Dockerfile.backend deploy/workers-kind
for task_image in postgres:18-alpine quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z krun-worker-builder:20261013 krun-worker-runtime:20261013 krun-workers-backend:20261013; do
  if ! docker image inspect "$task_image" >/dev/null 2>&1; then docker pull --platform linux/amd64 "$task_image"; fi
  docker save --platform linux/amd64 "$task_image" | docker exec -i "$task_node" ctr --namespace=k8s.io images import --platform linux/amd64 -
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
kubectl --kubeconfig "$KUBECONFIG" -n kgateway-system wait --for=condition=Programmed gateway/workers-mvp --timeout=120s
echo "Backend deployed in workers-mvp; existing workers-lab was not modified"
