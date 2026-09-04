#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: ./scripts/serve-temporary.sh [--listen ADDRESS]

Build, initialize, and serve an isolated MCP Gateway from this checkout.
All runtime files and the in-memory test keyring are discarded on shutdown.

Options:
  --listen ADDRESS  Numeric loopback authority (default: 127.0.0.1:8211)
  -h, --help        Show this help
EOF
}

listen="127.0.0.1:8211"
while (($#)); do
	case "$1" in
	--listen)
		if (($# < 2)); then
			printf 'The --listen flag requires an address.\n' >&2
			usage >&2
			exit 2
		fi
		listen="$2"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		printf 'Unknown argument: %s\n' "$1" >&2
		usage >&2
		exit 2
		;;
	esac
done

module_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
run_root="$(mktemp -d "${TMPDIR:-/tmp}/mcp-gateway-feature.XXXXXX")"
gateway_pid=""

cleanup() {
	status=$?
	trap - EXIT
	rm -rf -- "$run_root"
	exit "$status"
}

shutdown() {
	signal="$1"
	status="$2"
	trap '' HUP INT TERM
	if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
		kill -s "$signal" "$gateway_pid" 2>/dev/null || true
		wait "$gateway_pid" 2>/dev/null || true
	fi
	gateway_pid=""
	exit "$status"
}

trap cleanup EXIT
trap 'shutdown TERM 129' HUP
trap 'shutdown INT 130' INT
trap 'shutdown TERM 143' TERM

chmod 700 "$run_root"
binary="$run_root/mcp-gateway"
data_dir="$run_root/data"
bearer_file="$run_root/admin-bearer"
home_dir="$run_root/home"
mkdir "$home_dir"

printf 'Building temporary Gateway from %s\n' "$module_root"
go -C "$module_root" build -mod=readonly -tags=e2e \
	-o "$binary" ./cmd/mcp-gateway

MCP_GATEWAY_E2E_ACCOUNT_HOME="$home_dir" \
	"$binary" initialize \
	--data-dir "$data_dir" \
	--secret-output "$bearer_file"

printf '\nTemporary Gateway\n'
printf '  URL:          http://%s/\n' "$listen"
printf '  Run root:     %s\n' "$run_root"
printf '  Data:         %s\n' "$data_dir"
printf '  Admin bearer: %s\n' "$bearer_file"
printf 'Stop with Ctrl-C; all temporary state will be removed.\n\n'

set -m
MCP_GATEWAY_E2E_ACCOUNT_HOME="$home_dir" \
	"$binary" serve --data-dir "$data_dir" --listen "$listen" &
gateway_pid=$!
set +m

set +e
wait "$gateway_pid"
status=$?
set -e
gateway_pid=""
exit "$status"
