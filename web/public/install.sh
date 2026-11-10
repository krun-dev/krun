#!/bin/sh
set -eu

origin=${1:-}
if [ -z "$origin" ]; then
  echo "Usage: sh install.sh <krun-console-origin>" >&2
  exit 1
fi
origin=${origin%/}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "Unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

for command in curl python3; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required to install krun." >&2
    exit 1
  fi
done

protocol=$(python3 - "$origin" <<'PYTHON'
import ipaddress
import sys
from urllib.parse import urlsplit

origin = sys.argv[1]
try:
    url = urlsplit(origin)
    if url.port == 0:
        raise ValueError()
    if (not url.hostname or url.username is not None or url.path
            or "%" in url.netloc
            or "?" in origin or "#" in origin or "\\" in origin
            or any(character.isspace() for character in origin)):
        raise ValueError()
    if url.scheme == "http" and url.hostname != "localhost":
        if not ipaddress.ip_address(url.hostname).is_loopback:
            raise ValueError()
    if url.scheme not in ("http", "https"):
        raise ValueError()
except ValueError:
    sys.exit("Use an HTTPS console origin (HTTP is allowed for local development).")
print(url.scheme)
PYTHON
)

install_dir="$HOME/.local/bin"
mkdir -p "$install_dir"
staging=$(mktemp -d "$install_dir/.krun.XXXXXXXX")
trap 'rm -rf "$staging"' EXIT
trap 'exit 1' HUP INT TERM

download() {
  status=$(curl -q -fsS --globoff --proto "=$protocol" --max-filesize "$3" \
    --output "$2" --write-out '%{http_code}' -- "$origin/downloads/$1")
  if [ "$status" != 200 ]; then
    echo "Download of $1 returned HTTP $status. Only HTTP 200 is accepted." >&2
    exit 1
  fi
}

name="krun-$os-$arch"
download manifest.json "$staging/manifest.json" 1048576
download "$name" "$staging/$name" 134217728
python3 - "$staging" "$name" <<'PYTHON'
import hashlib
import json
import re
import sys
from pathlib import Path

directory = Path(sys.argv[1])
name = sys.argv[2]
try:
    manifest = json.loads((directory / "manifest.json").read_bytes())
    expected = manifest["files"][name]
    if not isinstance(expected, str) or not re.fullmatch("[0-9a-f]{64}", expected):
        raise ValueError()
except (KeyError, TypeError, ValueError):
    sys.exit(f"Missing or invalid SHA-256 in the manifest for {name}.")

digest = hashlib.sha256()
with (directory / name).open("rb") as binary:
    for block in iter(lambda: binary.read(1024 * 1024), b""):
        digest.update(block)
if digest.hexdigest() != expected:
    sys.exit(f"Checksum mismatch for {name}.")
PYTHON

temporary="$staging/$name"
chmod 755 "$temporary"
mv -f "$temporary" "$install_dir/krun"

echo "Installed krun to $install_dir/krun"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *)
    printf '\n%s\n' \
      'To use krun in this terminal, run:' \
      '  export PATH="$HOME/.local/bin:$PATH"' \
      'For future terminals, add the same line to ~/.zshrc (zsh) or ~/.bashrc (bash).'
    ;;
esac
echo "Run: krun login --endpoint $origin"
