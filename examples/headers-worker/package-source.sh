#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

rm -rf .build source.tar.gz
mkdir -p .build/src .build/assets
cp -R src/. .build/src/
cp -R public/. .build/assets/

# The builder accepts regular file entries only and strips the top-level src/
# and assets/ prefixes. Do not let tar add directory headers.
(cd .build && tar -czf ../source.tar.gz $(find src assets -type f | sort))

echo "wrote $(pwd)/source.tar.gz"
tar -tzf source.tar.gz
