#!/usr/bin/env bash
# Build the three application images. Pushing requires an explicit --push.
set -euo pipefail
cd "$(dirname "$0")/.."
registry=""
tag=""
platform="linux/amd64"
push=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --registry) registry="$2"; shift 2 ;;
    --tag) tag="$2"; shift 2 ;;
    --platform) platform="$2"; shift 2 ;;
    --push) push=true; shift ;;
    *) echo "usage: $0 --registry registry.example.com/team --tag RELEASE [--platform linux/amd64] [--push]" >&2; exit 2 ;;
  esac
done
if [[ -z "$registry" || ! "$tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ || "$tag" == latest || ! "$platform" =~ ^linux/(amd64|arm64)$ ]]; then
  echo "explicit registry, non-latest release tag and supported platform are required" >&2
  exit 2
fi
registry="${registry%/}"
for name in control runtime builder; do
  case "$name" in
    control) context="."; file="deploy/production/Dockerfile.control" ;;
    runtime) context="workers/runtime"; file="workers/runtime/Dockerfile" ;;
    builder) context="workers/builder"; file="workers/builder/Dockerfile" ;;
  esac
  docker build --platform "$platform" -f "$file" -t "$registry/krun-$name:$tag" "$context"
  if [[ "$push" == true ]]; then
    docker push "$registry/krun-$name:$tag"
  fi
done
