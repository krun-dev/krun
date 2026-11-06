#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
output="$root/dist/console/downloads"
mkdir -p "$output"

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name="krun-$os-$arch"
  if [[ "$os" == windows ]]; then
    name="$name.exe"
  fi
  echo "Building $name"
  (cd "$root" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -o "$output/$name" ./cmd/krun)
done
