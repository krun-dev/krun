#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
output="$root/dist/console/downloads"
mkdir -p "$output"
version=$(git -C "$root" describe --tags --always --dirty)
if [[ ! "$version" =~ ^[A-Za-z0-9._+-]+$ ]]; then
  echo "CLI version contains unsupported characters: $version" >&2
  exit 1
fi

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name="krun-$os-$arch"
  if [[ "$os" == windows ]]; then
    name="$name.exe"
  fi
  echo "Building $name"
  (cd "$root" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-X github.com/krun-dev/krun/internal/cli.Version=$version" \
    -o "$output/$name" ./cmd/krun)
done

# Publish the manifest last. A client seeing mixed files during an upload will
# reject their checksums and keep its existing executable.
python3 - "$output" "$version" <<'PY'
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
temporary.write_text(json.dumps({"version": sys.argv[2], "files": files}, indent=2) + "\n")
temporary.replace(manifest)
PY
