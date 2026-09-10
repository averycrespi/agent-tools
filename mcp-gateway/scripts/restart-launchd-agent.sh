#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

usage() {
  cat <<'EOF'
Usage: restart-launchd-agent.sh

Gracefully restart the default per-user Gateway LaunchAgent, or load it if absent.
Run as the intended logged-in macOS user, without sudo. Do not run concurrently
with other service management. Does not install binaries or change the plist.
EOF
}
fail() { printf '%s\n' "$*" >&2; exit 1; }
if [[ $# -gt 0 ]]; then
  case "$1" in
    --help|-h) [[ $# == 1 ]] || fail 'Unexpected arguments; see --help.'; usage; exit 0 ;;
    *) fail "Unknown argument: $1; see --help." ;;
  esac
fi

[[ "$(uname -s)" == Darwin ]] || fail 'This script requires macOS.'
uid="$(id -u)"
[[ "$uid" != 0 ]] || fail 'Run as the intended logged-in user, without sudo.'
account_home="$(dscl -plist . -read "/Users/$(id -un)" NFSHomeDirectory |
  plutil -extract dsAttrTypeStandard:NFSHomeDirectory.0 raw -o - -)"
[[ "$account_home" == /* && -d "$account_home" ]] || fail 'Cannot resolve OS-account home.'
label='dev.agent-tools.mcp-gateway'
domain="gui/$uid"
service="$domain/$label"
plist="$account_home/Library/LaunchAgents/$label.plist"
plutil -lint "$plist"
installed_label="$(plutil -extract Label raw -o - "$plist")"
[[ "$installed_label" == "$label" ]] || fail 'Plist label differs from the default service; use the manual procedure.'

# Only the specific absent-service diagnostic permits skipping bootout.
service_loaded() {
  if state="$(launchctl print "$service" 2>&1)"; then
    return 0
  fi
  case "$state" in
    *"Could not find service \"$label\" in domain"*) return 1 ;;
    *) fail "$state" ;;
  esac
}

if service_loaded; then
  pid="$(awk '$1 == "pid" && $2 == "=" { print $3; exit }' <<<"$state")"
  [[ -z "$pid" || "$pid" =~ ^[1-9][0-9]*$ ]] || fail 'Cannot parse Gateway PID; refusing to stop.'
  launchctl bootout "$service"
  if service_loaded; then
    fail 'Service is still loaded; refusing to bootstrap.'
  fi
  if [[ -n "$pid" ]]; then
    for ((i = 0; i <= 30; i++)); do
      if ps -p "$pid" -o pid= >/dev/null; then
        [[ "$i" -lt 30 ]] || fail "Gateway PID $pid has not exited; refusing to bootstrap. Inspect logs."
        sleep 1
      else
        status=$?
        [[ "$status" == 1 ]] || fail 'Cannot check old process; refusing to bootstrap.'
        break
      fi
    done
  fi
fi

launchctl bootstrap "$domain" "$plist"
printf 'Launch accepted. Readiness has not been checked; follow docs/operators/launchd.md#verify.\n'
