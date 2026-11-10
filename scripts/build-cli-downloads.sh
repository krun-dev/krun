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
  tag=$(git -C "$root" tag --points-at HEAD --list 'cli-[0-9]*')
  if [[ -n "$tag" ]]; then
    python3 - "$tag" <<'PY'
import re
import sys
from datetime import date

tag = re.fullmatch(r"cli-([1-9][0-9]{3}-[0-9]{2}-[0-9]{2})(?:\.([1-9][0-9]{0,8}))?", sys.argv[1])
try:
    if tag is None or tag[2] == "1":
        raise ValueError
    date.fromisoformat(tag[1])
except ValueError:
    sys.exit("Expected exactly one CLI release tag with a valid date (cli-YYYY-MM-DD or cli-YYYY-MM-DD.N, N >= 2)")
PY
    version="${tag#cli-}"
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
