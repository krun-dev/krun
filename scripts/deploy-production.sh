#!/usr/bin/env bash
# Render by default. Cluster writes require both --context and --apply.
set -euo pipefail
cd "$(dirname "$0")/.."
config=""
output=""
context=""
mode=render
while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) config="$2"; shift 2 ;;
    --out) output="$2"; shift 2 ;;
    --context) context="$2"; shift 2 ;;
    --apply) mode=apply; shift ;;
    --dry-run) mode=dry-run; shift ;;
    *) echo "usage: $0 --config MAIN_YAML --out PRIVATE_DIR [--context CONTEXT --dry-run|--apply]" >&2; exit 2 ;;
  esac
done
if [[ -z "$config" || -z "$output" ]]; then
  echo "a main YAML and private output directory are required" >&2
  exit 2
fi
go run ./cmd/krun-deploy --config "$config" --out "$output"
if [[ "$mode" == render ]]; then
  exit 0
fi
if [[ -z "$context" ]]; then
  echo "an explicit kubectl context is required; current-context is never inferred" >&2
  exit 2
fi
# Read only resource identities; never print the Secret manifest.
namespace="$(python3 - "$output/summary.json" <<'PYCODE'
import json,sys
v=json.load(open(sys.argv[1]))
print(next(x['metadata']['namespace'] for x in v['resources'] if x['kind']=='Deployment'))
PYCODE
)"
python3 - "$output/summary.json" "$context" "$output/gateway-check.json" <<'PYCODE'
import json,os,subprocess,sys
summary=json.load(open(sys.argv[1]))
items=[]
for gateway in summary['gateways']:
    raw=subprocess.check_output(['kubectl','--context',sys.argv[2],'-n',gateway['namespace'],'get','gateway',gateway['name'],'-o','json'])
    items.append(json.loads(raw))
fd=os.open(sys.argv[3],os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
with os.fdopen(fd,'w') as output:
    json.dump({'apiVersion':'v1','kind':'List','items':items},output)
PYCODE
go run ./cmd/krun-deploy --config "$config" --check-gateway "$output/gateway-check.json"
if [[ "$mode" == dry-run ]]; then
  # Kubernetes cannot server-validate namespaced objects in a nonexistent namespace.
  kubectl --context "$context" get namespace "$namespace" >/dev/null
  kubectl --context "$context" apply --server-side --dry-run=server --field-manager=krun-deploy -f "$output/resources.json"
  exit 0
fi
kubectl --context "$context" apply --server-side --field-manager=krun-deploy -f "$output/resources.json"
kubectl --context "$context" -n "$namespace" rollout status deployment --timeout=180s
