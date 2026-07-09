#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: ./check-llm-health.sh <bank_id>

Checks the Hindsight bank LLM health endpoint for an existing memory bank.
EOF
}

if [[ $# -ne 1 ]]; then
  usage
  exit 64
fi

bank_id=$1
script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
env_file=${HINDSIGHT_ENV_FILE:-"$script_dir/.env"}

if [[ ! -f "$env_file" ]]; then
  echo "Missing env file: $env_file" >&2
  echo "Copy .env.example to .env and configure HINDSIGHT_API_KEY first." >&2
  exit 66
fi

set -a
# shellcheck disable=SC1090
. "$env_file"
set +a

if [[ -z "${HINDSIGHT_API_KEY:-}" ]]; then
  echo "Set HINDSIGHT_API_KEY in $env_file." >&2
  exit 78
fi

api_url=${HINDSIGHT_API_URL:-http://localhost:8888}
api_url=${api_url%/}
encoded_bank_id=$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$bank_id")

curl -fsS \
  -X POST \
  -H "Authorization: Bearer $HINDSIGHT_API_KEY" \
  "$api_url/v1/default/banks/$encoded_bank_id/health/llm"
echo
