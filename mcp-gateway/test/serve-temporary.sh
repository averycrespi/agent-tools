#!/usr/bin/env bash
set -euo pipefail

module_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
runner="$module_root/scripts/serve-temporary.sh"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/mcp-gateway-serve-temporary-test.XXXXXX")"
runner_pid=""

cleanup() {
	if [[ -n "$runner_pid" ]]; then
		kill -TERM "$runner_pid" 2>/dev/null || true
		wait "$runner_pid" 2>/dev/null || true
	fi
	rm -rf -- "$test_root"
}
trap cleanup EXIT

mkdir "$test_root/bin"
cat >"$test_root/fake-gateway" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'pid=%s home=%s command=%s args=%s\n' "$$" "${MCP_GATEWAY_E2E_ACCOUNT_HOME:-}" "$1" "$*" >>"$MCP_GATEWAY_TEMP_TEST_LOG"
case "$1" in
initialize)
	while (($#)); do
		case "$1" in
		--data-dir) data_dir="$2"; shift 2 ;;
		--secret-output) bearer_file="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	mkdir -p "$data_dir"
	printf 'test-bearer\n' >"$bearer_file"
	chmod 600 "$bearer_file"
	;;
serve)
	printf 'Gateway started\n'
	trap 'printf "forwarded=TERM\n" >>"$MCP_GATEWAY_TEMP_TEST_LOG"; trap "" HUP INT TERM; sleep 0.2; exit 0' INT TERM
	trap 'printf "forwarded=HUP\n" >>"$MCP_GATEWAY_TEMP_TEST_LOG"; exit 9' HUP
	while :; do sleep 1; done
	;;
*) exit 2 ;;
esac
EOF
chmod +x "$test_root/fake-gateway"

cat >"$test_root/bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"$MCP_GATEWAY_TEMP_GO_LOG"
while (($#)); do
	if [[ "$1" == "-o" ]]; then
		cp "$MCP_GATEWAY_TEMP_FAKE_BINARY" "$2"
		chmod +x "$2"
		exit 0
	fi
	shift
done
exit 2
EOF
chmod +x "$test_root/bin/go"

export MCP_GATEWAY_TEMP_TEST_LOG="$test_root/gateway.log"
export MCP_GATEWAY_TEMP_GO_LOG="$test_root/go.log"
export MCP_GATEWAY_TEMP_FAKE_BINARY="$test_root/fake-gateway"
PATH="$test_root/bin:$PATH" "$runner" --listen 127.0.0.1:38211 >"$test_root/stdout" 2>"$test_root/stderr" &
runner_pid=$!

started=false
for _ in {1..100}; do
	if grep -qF 'Gateway started' "$test_root/stdout"; then
		started=true
		break
	fi
	sleep 0.05
done
if [[ "$started" != true ]]; then
	cat "$test_root/stdout" >&2
	cat "$test_root/stderr" >&2
	exit 1
fi

run_root="$(sed -n 's/^  Run root:     //p' "$test_root/stdout")"
[[ -n "$run_root" ]]
[[ -d "$run_root/data" ]]
[[ "$(stat -c '%a' "$run_root/admin-bearer" 2>/dev/null || stat -f '%Lp' "$run_root/admin-bearer")" == 600 ]]
grep -qF -- '-mod=readonly -tags=e2e' "$test_root/go.log"
grep -qF -- '--data-dir' "$test_root/gateway.log"
grep -qF -- '--listen 127.0.0.1:38211' "$test_root/gateway.log"
grep -qF -- "home=$run_root/home" "$test_root/gateway.log"
gateway_pid="$(sed -n 's/^pid=\([0-9][0-9]*\) .* command=serve .*/\1/p' "$test_root/gateway.log")"
[[ -n "$gateway_pid" ]]
runner_pgid="$(ps -o pgid= -p "$runner_pid" | tr -d ' ')"
gateway_pgid="$(ps -o pgid= -p "$gateway_pid" | tr -d ' ')"
[[ -n "$runner_pgid" && -n "$gateway_pgid" && "$runner_pgid" != "$gateway_pgid" ]]

kill -HUP "$runner_pid"
sleep 0.05
kill -HUP "$runner_pid" 2>/dev/null || true
set +e
wait "$runner_pid"
status=$?
set -e
runner_pid=""
[[ "$status" == 129 ]]
[[ ! -e "$run_root" ]]
grep -qF 'forwarded=TERM' "$test_root/gateway.log"
if grep -qF 'forwarded=HUP' "$test_root/gateway.log"; then
	exit 1
fi

grep -qF 'URL:          http://127.0.0.1:38211/' "$test_root/stdout"
grep -qF 'Admin bearer:' "$test_root/stdout"
grep -qF 'all temporary state will be removed' "$test_root/stdout"
