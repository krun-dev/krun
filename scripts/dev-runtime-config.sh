#!/usr/bin/env bash
# Render the local-first runtime from the main YAML. Managed local MinIO and
# signed S3-compatible origins share the same object-storage contract. The cluster
# keeps only workerd and kgateway; full in-cluster storage belongs to kind E2E.
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
: "${KRUN_DEV_CONTROL_HOST:?set KRUN_DEV_CONTROL_HOST in .data/dev.env}"
REMOTE="$KRUN_DEV_REMOTE"
KUBECONFIG_REMOTE="${KRUN_DEV_KUBECONFIG:-~/kind/kubeconfig}"
IMAGE="${KRUN_DEV_RUNTIME_IMAGE:-krun-worker-runtime:dev}"
tmp="$(mktemp -d)"
remote_tmp=""
cleanup_runtime_config() {
  rm -rf "$tmp"

  if [[ -n "$remote_tmp" ]]; then
    ssh "$REMOTE" "rm -rf -- $remote_tmp" >/dev/null 2>&1 || true
  fi
}
trap cleanup_runtime_config EXIT

go run ./cmd/krun-dev-runtime \
  --config .data/dev.yaml \
  --control-host "$KRUN_DEV_CONTROL_HOST" \
  --out "$tmp"
remote_tmp="$(ssh -o ConnectTimeout=8 "$REMOTE" 'umask 077; mktemp -d /tmp/krun-runtime-config.XXXXXXXX')"
if [[ ! "$remote_tmp" =~ ^/tmp/krun-runtime-config\.[A-Za-z0-9]+$ ]]; then
  exit 1
fi
scp "$tmp"/* deploy/workers-dev/validator.yaml "$REMOTE:$remote_tmp/"
touch .data/runtime-tunnel-refresh-needed
ssh -o ConnectTimeout=8 "$REMOTE" "set -e
  umask 077
  export KUBECONFIG=$KUBECONFIG_REMOTE
  kubectl -n workers-mvp apply -f '$remote_tmp/secrets.json'
  kubectl -n workers-mvp create configmap runtime-config --from-file=runtime.capnp='$remote_tmp/runtime.capnp' --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n workers-mvp create configmap validator-config --from-file=runtime.capnp='$remote_tmp/validator.capnp' --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -f '$remote_tmp/validator.yaml'
  kubectl apply -f '$remote_tmp/networkpolicy.json'
  kubectl -n workers-mvp patch deployment/runtime-default --type=strategic --patch-file '$remote_tmp/runtime-patch.json'
  kubectl -n workers-mvp set image deployment/runtime-default deployment/runtime-validator-default workerd='$IMAGE'
  kubectl -n workers-mvp scale deployment backend postgres minio git-mock tester --replicas=0
  kubectl -n workers-mvp rollout restart deployment/runtime-default deployment/runtime-validator-default
  kubectl -n workers-mvp rollout status deployment/runtime-default --timeout=180s
  kubectl -n workers-mvp rollout status deployment/runtime-validator-default --timeout=180s"
