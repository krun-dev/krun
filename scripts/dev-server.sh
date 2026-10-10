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
task_config="${1:-}"
if [[ -z "$task_config" ]]; then
  task_config="config/development.yaml"
  if [[ -f .data/sso-local.yaml ]]; then
    task_config=".data/sso-local.yaml"
  fi
fi
exec ./bin/krun-server serve --config "$task_config"
