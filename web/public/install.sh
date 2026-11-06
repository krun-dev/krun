#!/bin/sh
set -eu

origin=${1:-}
if [ -z "$origin" ]; then
  echo "Usage: sh install.sh <krun-console-origin>" >&2
  exit 1
fi
origin=${origin%/}

case "$origin" in
  https://* | http://localhost | http://localhost:* | http://127.0.0.1 | http://127.0.0.1:*) ;;
  *) echo "Use an HTTPS console origin (HTTP is allowed for local development)." >&2; exit 1 ;;
esac

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

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required to download krun." >&2
  exit 1
fi

install_dir="$HOME/.local/bin"
mkdir -p "$install_dir"
temporary=$(mktemp "$install_dir/.krun.XXXXXXXX")
trap 'rm -f "$temporary"' EXIT HUP INT TERM
curl -fsSL "$origin/downloads/krun-$os-$arch" -o "$temporary"
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
echo "Run: krun login --server $origin"
