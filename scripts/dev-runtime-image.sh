#!/usr/bin/env bash
# Rebuild only the runtime image. This is the one component that must run in
# the cluster; the control plane and builder run from source on the dev host.
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
: "${KRUN_DEV_CLUSTER:?set KRUN_DEV_CLUSTER in .data/dev.env}"
REMOTE="$KRUN_DEV_REMOTE"
KUBECONFIG_REMOTE="${KRUN_DEV_KUBECONFIG:-~/kind/kubeconfig}"
CLUSTER="$KRUN_DEV_CLUSTER"
IMAGE="${KRUN_DEV_RUNTIME_IMAGE:-krun-worker-runtime:dev}"

ssh -o ConnectTimeout=8 "$REMOTE" "rm -rf /tmp/krun-runtime && mkdir -p /tmp/krun-runtime"
scp workers/runtime/Dockerfile workers/runtime/entry.js workers/runtime/package.json workers/runtime/package-lock.json \
  "$REMOTE:/tmp/krun-runtime/"
ssh -o ConnectTimeout=8 "$REMOTE" "set -e
  cd /tmp/krun-runtime
  docker build -t '$IMAGE' .
  kind load docker-image --name '$CLUSTER' '$IMAGE'
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp set image deployment/runtime-default workerd='$IMAGE'
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp rollout restart deployment/runtime-default
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp rollout status deployment/runtime-default --timeout=180s"
