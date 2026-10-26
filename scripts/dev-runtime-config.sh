#!/usr/bin/env bash
# Point the cluster runtime at the local control plane and the single host
# MinIO, then remove the in-cluster backend/Postgres/MinIO/tester duplicates.
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
CONTROL_HOST="$KRUN_DEV_CONTROL_HOST"


tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# workerd's network allow-list is matched against the peer address after
# kube-proxy DNAT, so a ClusterIP never matches; allow the pod CIDR instead.
MINIO_POD_CIDR="${KRUN_DEV_POD_CIDR:-192.168.0.0/16}"
MINIO_SERVICE_CIDR="${KRUN_DEV_SERVICE_CIDR:-10.96.0.0/12}"
sed "s/__CONTROL_HOST__/$CONTROL_HOST/g; s|__MINIO_POD_CIDR__|$MINIO_POD_CIDR|g; s|__MINIO_SERVICE_CIDR__|$MINIO_SERVICE_CIDR|g" \
  deploy/workers-dev/runtime.capnp.tmpl >"$tmp/runtime.capnp"
sed "s/__CONTROL_HOST__/$CONTROL_HOST/g" \
  deploy/workers-dev/networkpolicy.yaml.tmpl >"$tmp/networkpolicy.yaml"

scp "$tmp/runtime.capnp" "$tmp/networkpolicy.yaml" "$REMOTE:/tmp/"
ssh -o ConnectTimeout=8 "$REMOTE" "set -e
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp create configmap runtime-config --from-file=runtime.capnp=/tmp/runtime.capnp --dry-run=client -o yaml | kubectl --kubeconfig $KUBECONFIG_REMOTE apply -f -
  kubectl --kubeconfig $KUBECONFIG_REMOTE apply -f /tmp/networkpolicy.yaml
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp scale deployment backend postgres tester --replicas=0
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp scale deployment minio --replicas=1
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp rollout restart deployment/runtime-default
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp rollout status deployment/runtime-default --timeout=180s"
