#!/usr/bin/env bash
# Produce an OSS-only SPA artifact. This never starts a frontend server or uploads.
set -euo pipefail
cd "$(dirname "$0")/.."
output="${1:-.data/console-release}"
mkdir -p "$output"
npm run build
make check-static-build
python3 - "$output" <<'PY'
from pathlib import Path
import sys,tarfile
root=Path('dist/console')
output=Path(sys.argv[1])/'console.tar.gz'
with tarfile.open(output,'w:gz') as archive:
    for item in sorted(root.iterdir()):
        archive.add(item, arcname=item.name)
print('Static console archive:',output)
PY
