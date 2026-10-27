#!/usr/bin/env bash
# Fail if local dev secrets or internal hosts appear in tracked files.
# Values come from the gitignored .data/dev.yaml and .data/dev.env.
set -euo pipefail
cd "$(dirname "$0")/.."

files=()
while IFS= read -r -d '' file; do
  files+=("$file")
done < <(git ls-files -z --cached --others --exclude-standard)

fail=0
check_value() {
  local label="$1" value="$2"
  [[ -z "$value" ]] && return 0
  for file in "${files[@]}"; do
    if grep -F -I -q -- "$value" "$file" 2>/dev/null; then
      echo "secret leak: $label in $file"
      fail=1
    fi
  done
}

if [[ -f .data/dev.env ]]; then
  for key in KRUN_DEV_REMOTE KRUN_DEV_CONTROL_HOST KRUN_DEV_MINIO_HOST; do
    value="$(sed -n "s/^${key}=//p" .data/dev.env | head -1)"
    check_value "$key" "$value"
  done
fi

if [[ -f .data/dev.yaml ]]; then
  for key in runtime_token management_token s3_secret_key external_domain; do
    value="$(sed -n "s/^[[:space:]]*${key}:[[:space:]]*//p" .data/dev.yaml | head -1 | sed 's/^"//; s/"$//')"
    check_value "$key" "$value"
  done
  db="$(sed -n 's/^db:[[:space:]]*//p' .data/dev.yaml | head -1 | sed 's/^"//; s/"$//')"
  password="$(printf '%s' "$db" | sed -n 's#.*://[^:]*:\([^@]*\)@.*#\1#p')"
  check_value "database password" "$password"
  minio="$(sed -n 's/^[[:space:]]*s3_endpoint:[[:space:]]*//p' .data/dev.yaml | head -1 | sed 's/^"//; s/"$//; s#^[a-z]*://##; s#/.*##; s#:.*##')"
  case "$minio" in
    ""|127.0.0.1|localhost|::1) ;;
    *) check_value "minio host" "$minio" ;;
  esac
fi

for file in "${files[@]}"; do
  if grep -E -I -q -- "-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----|AKIA[0-9A-Z]{16}" "$file" 2>/dev/null; then
    echo "secret pattern: $file"
    fail=1
  fi
done

if [[ "$fail" -ne 0 ]]; then
  echo "keep local values in .data/ (gitignored); tracked files must use placeholders" >&2
  exit 1
fi
echo "no local secrets or internal hosts found in tracked files"
