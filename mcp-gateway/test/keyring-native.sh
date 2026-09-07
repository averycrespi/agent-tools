#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OS=$(go env GOOS)
LOG=$(mktemp)
trap 'rm -f "$LOG"' EXIT

emit() {
	local result=$1 reason=$2 deterministic=$3 native=$4 raw status
	raw=$(go -C "$ROOT" run ./test/keyringnative/cmd emit "$result" "$OS" "$reason" "$deterministic" "$native")
	set +e
	printf '%s\n' "$raw" | go -C "$ROOT" run ./test/keyringnative/cmd validate
	status=$?
	set -e
	exit "$status"
}

if [[ "${MCP_GATEWAY_KEYRING_NATIVE_SELF_TEST:-}" == "1" ]]; then
	case "${MCP_GATEWAY_KEYRING_NATIVE_FORCE_RESULT:-}" in
	passed) emit passed self_test_passed passed passed ;;
	skipped) emit skipped self_test_skipped passed skipped ;;
	failed) emit failed self_test_failed passed failed ;;
	*) emit failed self_test_invalid passed failed ;;
	esac
fi

if ! go -C "$ROOT" run ./test/acceptance/cmd run-suite test-material >"$LOG" 2>&1; then
	emit failed deterministic_material_failed failed skipped
fi

run_native_test() {
	MCP_GATEWAY_KEYRING_NATIVE=1 go -C "$ROOT" run ./test/acceptance/cmd run-suite test-keyring-native >"$LOG" 2>&1
}

run_linux() {
	if ! command -v dbus-run-session >/dev/null || ! command -v gnome-keyring-daemon >/dev/null; then
		emit skipped linux_prerequisites_unavailable passed skipped
	fi
	local sandbox status
	sandbox=$(mktemp -d)
	trap 'rm -rf "$sandbox"; rm -f "$LOG"' EXIT
	mkdir -m 700 "$sandbox/home" "$sandbox/runtime" "$sandbox/data"
	set +e
	dbus-run-session -- env \
		HOME="$sandbox/home" \
		XDG_RUNTIME_DIR="$sandbox/runtime" \
		XDG_DATA_HOME="$sandbox/data" \
		MCP_GATEWAY_NATIVE_ROOT="$ROOT" \
		bash -euo pipefail -c '
			eval "$(printf "%s\n" mcp-gateway-test | gnome-keyring-daemon --unlock --components=secrets)"
			trap '\''if [[ -n "${GNOME_KEYRING_PID:-}" ]]; then kill "$GNOME_KEYRING_PID" 2>/dev/null || true; fi'\'' EXIT
			MCP_GATEWAY_KEYRING_NATIVE=1 go -C "$MCP_GATEWAY_NATIVE_ROOT" run ./test/acceptance/cmd run-suite test-keyring-native
		' >"$LOG" 2>&1
	status=$?
	set -e
	if [[ $status -eq 0 ]]; then
		emit passed linux_secret_service_passed passed passed
	fi
	emit failed native_test_failed passed failed
}

DARWIN_SANDBOX=
DARWIN_KEYCHAIN=
DARWIN_OLD_DEFAULT=
DARWIN_OLD_LIST=

restore_darwin() {
	local status=0
	if [[ -n "$DARWIN_OLD_DEFAULT" ]]; then
		security default-keychain -d user -s "$DARWIN_OLD_DEFAULT" >"$LOG" 2>&1 || status=1
	fi
	if [[ -n "$DARWIN_OLD_LIST" ]]; then
		python3 - "$DARWIN_OLD_LIST" <<'PY' >"$LOG" 2>&1 || status=1
import shlex
import subprocess
import sys
with open(sys.argv[1], encoding="utf-8") as source:
    keychains = shlex.split(source.read())
if keychains:
    subprocess.run(["security", "list-keychains", "-d", "user", "-s", *keychains], check=True)
PY
	fi
	if [[ -n "$DARWIN_KEYCHAIN" ]]; then
		security delete-keychain "$DARWIN_KEYCHAIN" >"$LOG" 2>&1 || true
	fi
	return "$status"
}

interrupt_darwin() {
	trap - HUP INT TERM
	restore_darwin || true
	emit failed macos_interrupted passed failed
}

run_darwin() {
	if [[ "${MCP_GATEWAY_DISPOSABLE_MACOS_KEYCHAIN:-}" != "1" ]]; then
		emit skipped macos_disposable_keychain_required passed skipped
	fi
	local password status=0
	DARWIN_SANDBOX=$(mktemp -d)
	DARWIN_KEYCHAIN="$DARWIN_SANDBOX/mcp-gateway-test.keychain-db"
	password=mcp-gateway-test
	trap 'rm -rf "$DARWIN_SANDBOX"; rm -f "$LOG"' EXIT
	DARWIN_OLD_DEFAULT=$(security default-keychain -d user | tr -d '"') || emit failed macos_setup_failed passed failed
	DARWIN_OLD_LIST="$DARWIN_SANDBOX/old-keychains.txt"
	security list-keychains -d user >"$DARWIN_OLD_LIST" || emit failed macos_setup_failed passed failed
	trap interrupt_darwin HUP INT TERM
	set +e
	security create-keychain -p "$password" "$DARWIN_KEYCHAIN" >"$LOG" 2>&1 &&
		security set-keychain-settings -lut 3600 "$DARWIN_KEYCHAIN" >"$LOG" 2>&1 &&
		security unlock-keychain -p "$password" "$DARWIN_KEYCHAIN" >"$LOG" 2>&1 &&
		security default-keychain -d user -s "$DARWIN_KEYCHAIN" >"$LOG" 2>&1 &&
		security list-keychains -d user -s "$DARWIN_KEYCHAIN" >"$LOG" 2>&1
	status=$?
	set -e
	if [[ $status -eq 0 ]] && ! run_native_test; then
		status=1
	elif [[ $status -ne 0 ]]; then
		status=3
	fi
	trap - HUP INT TERM
	if ! restore_darwin; then
		status=2
	fi
	if [[ $status -eq 0 ]]; then
		emit passed macos_keychain_passed passed passed
	elif [[ $status -eq 2 ]]; then
		emit failed macos_cleanup_failed passed failed
	elif [[ $status -eq 3 ]]; then
		emit failed macos_setup_failed passed failed
	fi
	emit failed native_test_failed passed failed
}

case "$OS" in
linux) run_linux ;;
darwin) run_darwin ;;
*) emit skipped unsupported_platform passed skipped ;;
esac
