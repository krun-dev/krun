#!/usr/bin/env bash
# Point the cluster runtime and static-assets Service at the control plane and
# MinIO running on the development machine. The cluster keeps only workerd and
# kgateway; the full in-cluster stack belongs to deploy/workers-kind E2E.
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
: "${KRUN_DEV_MINIO_HOST:?set KRUN_DEV_MINIO_HOST in .data/dev.env}"

REMOTE="$KRUN_DEV_REMOTE"
KUBECONFIG_REMOTE="${KRUN_DEV_KUBECONFIG:-~/kind/kubeconfig}"
CONTROL_HOST="$KRUN_DEV_CONTROL_HOST"
MINIO_HOST="$KRUN_DEV_MINIO_HOST"
MINIO_PORT="${KRUN_DEV_MINIO_PORT:-9000}"
S3_BUCKET="${KRUN_DEV_S3_BUCKET:-krun-artifacts}"

for address in "$CONTROL_HOST" "$MINIO_HOST"; do
  if [[ ! "$address" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    echo "KRUN_DEV_CONTROL_HOST and KRUN_DEV_MINIO_HOST must be IPv4 addresses reachable from kind" >&2
    exit 1
  fi
done
if [[ ! "$MINIO_PORT" =~ ^[0-9]+$ ]] || (( MINIO_PORT < 1 || MINIO_PORT > 65535 )); then
  echo "KRUN_DEV_MINIO_PORT must be a TCP port" >&2
  exit 1
fi
if [[ ! "$S3_BUCKET" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]]; then
  echo "KRUN_DEV_S3_BUCKET must be a valid S3 bucket name" >&2
  exit 1
fi


tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# Business runtimes fetch artifacts from the development-machine MinIO address
# directly; the selectorless Service below exists for the Gateway static domain.
# Allow only the two explicit development-machine addresses in workerd.
sed "s/__CONTROL_HOST__/$CONTROL_HOST/g; s/__MINIO_HOST__/$MINIO_HOST/g; s/__MINIO_PORT__/$MINIO_PORT/g; s/__S3_BUCKET__/$S3_BUCKET/g" \
  deploy/workers-dev/runtime.capnp.tmpl >"$tmp/runtime.capnp"
sed "s/__CONTROL_HOST__/$CONTROL_HOST/g; s/__MINIO_HOST__/$MINIO_HOST/g; s/__MINIO_PORT__/$MINIO_PORT/g" \
  deploy/workers-dev/networkpolicy.yaml.tmpl >"$tmp/networkpolicy.yaml"
sed "s/__MINIO_HOST__/$MINIO_HOST/g; s/__MINIO_PORT__/$MINIO_PORT/g" \
  deploy/workers-dev/static-assets.yaml.tmpl >"$tmp/static-assets.yaml"

scp "$tmp/runtime.capnp" "$tmp/networkpolicy.yaml" "$tmp/static-assets.yaml" "$REMOTE:/tmp/"
ssh -o ConnectTimeout=8 "$REMOTE" "set -e
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp create configmap runtime-config --from-file=runtime.capnp=/tmp/runtime.capnp --dry-run=client -o yaml | kubectl --kubeconfig $KUBECONFIG_REMOTE apply -f -
  kubectl --kubeconfig $KUBECONFIG_REMOTE apply -f /tmp/networkpolicy.yaml
  kubectl --kubeconfig $KUBECONFIG_REMOTE apply -f /tmp/static-assets.yaml
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp scale deployment backend postgres minio tester --replicas=0
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp rollout restart deployment/runtime-default
  kubectl --kubeconfig $KUBECONFIG_REMOTE -n workers-mvp rollout status deployment/runtime-default --timeout=180s"
