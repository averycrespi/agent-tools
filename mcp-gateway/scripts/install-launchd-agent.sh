#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: install-launchd-agent.sh [--binary ABSOLUTE_PATH] [--data-dir ABSOLUTE_PATH]

Install a private per-user Gateway plist without initializing or starting Gateway.
Defaults: binary from $(go env GOPATH)/bin; data from absolute XDG_DATA_HOME or
OS-account home/.local/share/mcp-gateway. Existing plists are never overwritten.
Run as the intended logged-in macOS user, without sudo.
EOF
}
fail() { printf '%s\n' "$*" >&2; exit 1; }
absolute() { [[ "$2" == /* ]] || fail "$1 must be an absolute path: $2"; }

binary=''
data=''
while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --binary|--data-dir)
      [[ $# -ge 2 && -n "$2" ]] || fail "Missing path for $1; see --help."
      absolute "$1" "$2"
      if [[ "$1" == --binary ]]; then binary="$2"; else data="$2"; fi
      shift 2 ;;
    *) fail "Unknown argument: $1; see --help." ;;
  esac
done

[[ "$(uname -s)" == Darwin ]] || fail 'This installer requires macOS.'
uid="$(id -u)"
[[ "$uid" != 0 ]] || fail 'Run as the intended logged-in user, without sudo.'
account_home="$(dscl -plist . -read "/Users/$(id -un)" NFSHomeDirectory |
  plutil -extract dsAttrTypeStandard:NFSHomeDirectory.0 raw -o - -)"
absolute 'OS-account home' "$account_home"
[[ -d "$account_home" ]] || fail 'OS-account home is not a directory.'

if [[ -z "$binary" ]]; then
  gopath="$(go env GOPATH)"
  [[ "$gopath" != *:* ]] || fail 'Multiple GOPATH entries; select --binary explicitly.'
  binary="$gopath/bin/mcp-gateway"
fi
absolute 'Binary' "$binary"
[[ -f "$binary" && -x "$binary" ]] || fail "Gateway executable not found: $binary (run make install or use --binary)."
if [[ -z "$data" ]]; then
  if [[ -n "${XDG_DATA_HOME:-}" ]]; then
    absolute 'XDG_DATA_HOME' "$XDG_DATA_HOME"
    data="${XDG_DATA_HOME%/}/mcp-gateway"
  else
    data="$account_home/.local/share/mcp-gateway"
  fi
fi

label='dev.agent-tools.mcp-gateway'
agents="$account_home/Library/LaunchAgents"
logs="$account_home/Library/Logs/mcp-gateway"
plist="$agents/$label.plist"
[[ ! -e "$plist" && ! -L "$plist" ]] || fail "Plist already exists; follow the guide's plist-change procedure: $plist"
umask 077
for directory in "$account_home/Library" "$agents" "$account_home/Library/Logs" "$logs"; do
  [[ ! -L "$directory" ]] || fail "Refusing symlink directory: $directory"
  if [[ ! -e "$directory" ]]; then mkdir "$directory"; fi
  [[ -d "$directory" && -O "$directory" ]] || fail "Directory must be owned by this user: $directory"
  [[ -n "$(find "$directory" -prune ! -perm -0020 ! -perm -0002 -print)" ]] || fail "Directory is writable by other users; inspect permissions: $directory"
done
[[ -n "$(find "$logs" -prune -perm 0700 -print)" ]] || fail "Log directory must have mode 0700; inspect permissions: $logs"
for log in "$logs/stdout.log" "$logs/stderr.log"; do
  if [[ -e "$log" || -L "$log" ]]; then
    [[ -f "$log" && ! -L "$log" && -O "$log" ]] || fail "Log must be a regular file owned by this user: $log"
    [[ -n "$(find "$log" -prune -perm 0600 -print)" ]] || fail "Log must have mode 0600; inspect permissions: $log"
  else
    (set -o noclobber; : > "$log")
  fi
done

source_dir="$(dirname -- "${BASH_SOURCE[0]}")"
temporary="$(mktemp "$agents/.mcp-gateway.XXXXXX")"
trap 'rm -f "$temporary"' EXIT
cp "$source_dir/../examples/launchd/mcp-gateway.plist" "$temporary"
chmod 600 "$temporary"
plutil -replace ProgramArguments.0 -string "$binary" "$temporary"
plutil -replace ProgramArguments.3 -string "$data" "$temporary"
plutil -replace StandardOutPath -string "$logs/stdout.log" "$temporary"
plutil -replace StandardErrorPath -string "$logs/stderr.log" "$temporary"
plutil -lint "$temporary"
# Stage validation first; noclobber also refuses a destination created meanwhile.
(set -o noclobber; cat "$temporary" > "$plist")

printf 'Installed: %s\nExecutable: %s\nData directory: %s\nLogs: %s\n' "$plist" "$binary" "$data" "$logs"
printf '\nNo state initialized and no service started. Stop any existing Gateway first.\n'
printf 'Load: launchctl bootstrap %q %q\n' "gui/$uid" "$plist"
printf 'Verify: %q --data-dir %q status\n' "$binary" "$data"
