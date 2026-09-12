#!/bin/bash
# Configure a Lima sandbox's Pi MCP Gateway client, not the host service.
# The host must run `mcp-gateway serve --allowed-host host.lima.internal`.
# Issue a dedicated principal's agent credential on the host, then configure:
#
#   "copy_paths": ["~/.config/mcp-gateway/agent-token"]
#
# sb provision refreshes copy_paths before scripts. Never copy administrator
# credentials or the Gateway data directory. See docs/operators/access-control.md.

set -euo pipefail

TOKEN_FILE="$HOME/.config/mcp-gateway/agent-token"

if [[ ! -f "$TOKEN_FILE" || ! -r "$TOKEN_FILE" || ! -s "$TOKEN_FILE" ]]; then
	cat >&2 <<EOF
error: missing, unreadable, or empty $TOKEN_FILE.

On the host, issue a dedicated principal's agent credential using:
  mcp-gateway principal credential issue PRINCIPAL_ID --secret-output "$TOKEN_FILE" --yes
The output path must be fresh. Add ~/.config/mcp-gateway/agent-token to
sandbox-manager's copy_paths, then run sb provision again.

See the Gateway access-control guide for credential issuance and rotation.
EOF
	exit 1
fi

MARKER_START="# >>> mcp-gateway >>>"
MARKER_END="# <<< mcp-gateway <<<"
BASHRC="$HOME/.bashrc"

touch "$BASHRC"

# Replace the whole block so newer script versions reach existing sandboxes.
if grep -qF "$MARKER_START" "$BASHRC"; then
	sed -i "/^${MARKER_START}$/,/^${MARKER_END}$/d" "$BASHRC"
fi

# Keep the managed block separate when an existing file lacks a final newline.
if [[ -s "$BASHRC" && -n "$(tail -c 1 "$BASHRC")" ]]; then
	printf '\n' >>"$BASHRC"
fi

cat >>"$BASHRC" <<'EOF'
# >>> mcp-gateway >>>
export MCP_GATEWAY_ENDPOINT="http://host.lima.internal:8210/mcp"
export MCP_GATEWAY_AGENT_TOKEN="$(cat "$HOME/.config/mcp-gateway/agent-token")"
# <<< mcp-gateway <<<
EOF

echo "Configured. Open a new shell, or run: source \"$BASHRC\""
echo "Restart Pi from that shell to pick up the endpoint and current agent token."
