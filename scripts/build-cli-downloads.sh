#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
output="$root/dist/console/downloads"
mkdir -p "$output"
commit=$(git -C "$root" rev-parse --short=12 HEAD)
version="dev+$commit"
published_at=""
if [[ -n "$(git -C "$root" status --porcelain)" ]]; then
  version+=".dirty"
else
  tag=$(git -C "$root" tag --points-at HEAD --list 'cli-v*')
  if [[ -n "$tag" ]]; then
    if [[ ! "$tag" =~ ^cli-v[1-9][0-9]{3}\.([1-9]|1[0-2])\.[1-9][0-9]{0,8}$ ]]; then
      echo "Expected exactly one CLI release tag in the form cli-vYYYY.M.N" >&2
      exit 1
    fi
    version="${tag#cli-v}"
    published_at=$(git -C "$root" for-each-ref --format='%(creatordate:iso-strict)' "refs/tags/$tag")
  fi
fi

cli_package="github.com/krun-dev/krun/internal/cli"
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name="krun-$os-$arch"
  if [[ "$os" == windows ]]; then
    name="$name.exe"
  fi
  echo "Building $name"
  (cd "$root" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-X $cli_package.Version=$version -X $cli_package.buildCommit=$commit -X $cli_package.publishedAt=${published_at:-unreleased}" \
    -o "$output/$name" ./cmd/krun)
done

# Publish the manifest last. A client seeing mixed files during an upload will
# reject their checksums and keep its existing executable.
python3 - "$output" "$version" "$commit" "$published_at" <<'PY'
import hashlib
import json
import sys
from pathlib import Path

output = Path(sys.argv[1])
files = {
    path.name: hashlib.sha256(path.read_bytes()).hexdigest()
    for path in sorted(output.glob("krun-*"))
    if path.is_file()
}
manifest = output / "manifest.json"
temporary = output / ".manifest.json.tmp"
release = {
    "version": sys.argv[2],
    "commit": sys.argv[3],
    "published_at": sys.argv[4] or None,
    "files": files,
}
temporary.write_text(json.dumps(release, indent=2) + "\n")
temporary.replace(manifest)
PY
