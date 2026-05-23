#!/bin/bash
# Configure a Lima sandbox to talk to the host's mcp-broker over HTTP +
# bearer auth.
#
# Depends on the host running mcp-broker (either manually via
# `mcp-broker serve` or via the launchd agent; see docs/launchd.md). That
# process writes an auth token to $HOME/.config/mcp-broker/auth-token on
# the host.
#
# The sandbox needs that file. This script does NOT read it from a host
# mount — the sandbox-manager `copy_paths` config must ship it in. Example
# config entry (paired with the `scripts` entry that invokes this file):
#
#   "copy_paths": [
#     "~/.config/mcp-broker/auth-token"
#   ]
#
# Paths starting with `~/` expand to the user home, so the file lands at
# the same path inside the sandbox. sb provision re-runs copy_paths before
# the scripts, so token rotation is picked up transparently on the next
# provision.
#
# This script writes a marker-fenced env block to ~/.bashrc that exports
# MCP_BROKER_URL and MCP_BROKER_TOKEN. Wire those into your agent's MCP
# config (e.g. Claude Code's `claude mcp add`). The block is idempotent —
# re-running this script after the token rotates is safe; the export reads
# the token from the file at shell startup, so a re-provision (which
# refreshes the file via copy_paths) is enough to pick up rotation.

set -euo pipefail

TOKEN_FILE="$HOME/.config/mcp-broker/auth-token"

if [[ ! -r "$TOKEN_FILE" ]]; then
	cat >&2 <<EOF
error: missing $TOKEN_FILE.

This file is created on the host by mcp-broker on first launch and must be
copied into the sandbox via sandbox-manager's copy_paths config.

See the mcp-broker docs for more information.
EOF
	exit 1
fi

MARKER_START="# >>> mcp-broker >>>"
MARKER_END="# <<< mcp-broker <<<"

if grep -qF "$MARKER_START" "$HOME/.bashrc" 2>/dev/null; then
	echo "mcp-broker env already configured in ~/.bashrc, skipping"
	exit 0
fi

echo "Configuring MCP_BROKER_URL and MCP_BROKER_TOKEN in ~/.bashrc"
cat >>"$HOME/.bashrc" <<EOF

$MARKER_START
# Point agent MCP clients at the host's mcp-broker. Lima's default
# user-mode networking forwards host.lima.internal to the host loopback,
# where the broker listens on :8200.
export MCP_BROKER_URL="http://host.lima.internal:8200/mcp"
# Bearer token was copied in from the host by sandbox-manager's copy_paths.
# Reading it here (rather than embedding the value) means token rotation is
# picked up automatically after the next sb provision.
export MCP_BROKER_TOKEN="\$(tr -d '\n' < \$HOME/.config/mcp-broker/auth-token)"
$MARKER_END
EOF
