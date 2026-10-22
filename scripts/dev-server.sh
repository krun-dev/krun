#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -z "${KRUN_GITLAB_SECRET:-}" && -f .data/gitlab-local.secret ]]; then
  export KRUN_GITLAB_SECRET="$(cat .data/gitlab-local.secret)"
fi
if [[ -z "${KRUN_GITHUB_SECRET:-}" && -f .data/github-local.secret ]]; then
  export KRUN_GITHUB_SECRET="$(cat .data/github-local.secret)"
fi
if [[ -z "${KRUN_ALIYUN_SECRET:-}" && -f .data/aliyun-local.secret ]]; then
  export KRUN_ALIYUN_SECRET="$(cat .data/aliyun-local.secret)"
fi
if [[ -z "${KRUN_SMTP_PASSWORD:-}" && -f .data/smtp-local.secret ]]; then
  export KRUN_SMTP_PASSWORD="$(cat .data/smtp-local.secret)"
fi
# config/development.yaml includes a workers: block, so the controller needs a
# Kubernetes service account. Point it at the kind credentials when present.
if [[ -z "${KRUN_KUBE_SA_DIR:-}" && -d .data/kind-creds ]]; then
  export KRUN_KUBE_SA_DIR="$PWD/.data/kind-creds"
  export KRUN_KUBE_CA="$PWD/.data/kind-creds/ca.crt"
  export KRUN_KUBE_API="${KRUN_KUBE_API:-https://127.0.0.1:38443}"
fi
# Single gitignored local dev config: control plane + Postgres + MinIO + builder
# run here; only the runtime (workerd) and kgateway stay in the cluster.
task_config="${1:-.data/dev.yaml}"
exec ./bin/krun-server serve --config "$task_config"
