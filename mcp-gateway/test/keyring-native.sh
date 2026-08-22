#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OS=$(go env GOOS)

run_test() {
	MCP_GATEWAY_KEYRING_NATIVE=1 go -C "$ROOT" test -race -tags=keyringnative -run '^TestNativeDisposableKeyringRoundTrip$' ./internal/keyring
}

run_linux() {
	if ! command -v dbus-run-session >/dev/null || ! command -v gnome-keyring-daemon >/dev/null; then
		echo "SKIP keyring-native: isolated Linux Secret Service prerequisites are unavailable"
		return
	fi
	local sandbox
	sandbox=$(mktemp -d)
	trap 'rm -rf "$sandbox"' RETURN
	mkdir -m 700 "$sandbox/home" "$sandbox/runtime" "$sandbox/data"
	dbus-run-session -- env \
		HOME="$sandbox/home" \
		XDG_RUNTIME_DIR="$sandbox/runtime" \
		XDG_DATA_HOME="$sandbox/data" \
		MCP_GATEWAY_NATIVE_ROOT="$ROOT" \
		bash -euo pipefail -c '
			eval "$(printf "%s\n" mcp-gateway-test | gnome-keyring-daemon --unlock --components=secrets)"
			trap '\''if [[ -n "${GNOME_KEYRING_PID:-}" ]]; then kill "$GNOME_KEYRING_PID" 2>/dev/null || true; fi'\'' EXIT
			MCP_GATEWAY_KEYRING_NATIVE=1 go -C "$MCP_GATEWAY_NATIVE_ROOT" test -race -tags=keyringnative -run '\''^TestNativeDisposableKeyringRoundTrip$'\'' ./internal/keyring
		'
}

restore_darwin_keychains() {
	local old_default=$1
	local old_list=$2
	local status=0
	security default-keychain -d user -s "$old_default" >/dev/null 2>&1 || status=1
	python3 - "$old_list" <<'PY' || status=1
import shlex
import subprocess
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    keychains = shlex.split(source.read())
if keychains:
    subprocess.run(
        ["security", "list-keychains", "-d", "user", "-s", *keychains],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
PY
	return "$status"
}

DARWIN_SANDBOX=
DARWIN_KEYCHAIN=
DARWIN_OLD_DEFAULT=
DARWIN_OLD_LIST=

cleanup_darwin() {
	local status=0
	restore_darwin_keychains "$DARWIN_OLD_DEFAULT" "$DARWIN_OLD_LIST" || status=1
	security delete-keychain "$DARWIN_KEYCHAIN" >/dev/null 2>&1 || true
	rm -rf "$DARWIN_SANDBOX"
	return "$status"
}

exit_darwin() {
	local status=$?
	trap - EXIT HUP INT TERM
	cleanup_darwin || status=1
	exit "$status"
}

run_darwin() {
	if [[ "${MCP_GATEWAY_DISPOSABLE_MACOS_KEYCHAIN:-}" != "1" ]]; then
		echo "SKIP keyring-native: macOS requires an explicitly disposable login-keychain context"
		return
	fi
	local password
	DARWIN_SANDBOX=$(mktemp -d)
	DARWIN_KEYCHAIN="$DARWIN_SANDBOX/mcp-gateway-test.keychain-db"
	password=mcp-gateway-test
	DARWIN_OLD_DEFAULT=$(security default-keychain -d user | tr -d '"')
	DARWIN_OLD_LIST="$DARWIN_SANDBOX/old-keychains.txt"
	security list-keychains -d user >"$DARWIN_OLD_LIST"
	trap exit_darwin EXIT
	trap 'exit 129' HUP
	trap 'exit 130' INT
	trap 'exit 143' TERM
	security create-keychain -p "$password" "$DARWIN_KEYCHAIN"
	security set-keychain-settings -lut 3600 "$DARWIN_KEYCHAIN"
	security unlock-keychain -p "$password" "$DARWIN_KEYCHAIN"
	security default-keychain -d user -s "$DARWIN_KEYCHAIN"
	security list-keychains -d user -s "$DARWIN_KEYCHAIN"
	run_test
}

case "$OS" in
linux)
	run_linux
	;;
darwin)
	run_darwin
	;;
*)
	echo "SKIP keyring-native: unsupported OS $OS"
	;;
esac
