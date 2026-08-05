#!/bin/bash
# Configure a Lima sandbox to route HTTP and HTTPS through the host's
# http-broker, and to trust the certificate it presents.
#
# Depends on the host running http-broker (via `http-broker serve` or the
# launchd agent; see docs/launchd.md). That process writes an auth token to
# $HOME/.config/http-broker/auth-token and a CA certificate to
# $HOME/.local/share/http-broker/ca.pem on the host.
#
# The sandbox needs both files. This script does NOT fetch them over the
# network and does NOT read them from a host mount — sandbox-manager's
# `copy_paths` must ship them in:
#
#   "copy_paths": [
#     "~/.config/http-broker/auth-token",
#     "~/.local/share/http-broker/ca.pem"
#   ]
#
# Paths starting with `~/` expand to the user home, so each file lands at the
# same path inside the sandbox. `sb provision` re-runs copy_paths before the
# scripts, so a token rotation or a CA rotation is picked up on the next
# provision.
#
# Re-running this script is safe: the ~/.bashrc block is marker-fenced and
# replaced wholesale, and the CA install is idempotent.

set -euo pipefail

TOKEN_FILE="$HOME/.config/http-broker/auth-token"
CA_SOURCE="$HOME/.local/share/http-broker/ca.pem"

# Where the guest trust store lives. Ubuntu is the sandbox-manager guest image.
CA_DEST="${HTTP_BROKER_CA_DEST:-/usr/local/share/ca-certificates/http-broker.crt}"
UPDATE_CA="${HTTP_BROKER_UPDATE_CA:-update-ca-certificates}"

# The system trust store is root-owned and `sb` runs provisioning scripts as
# the unprivileged guest user, so both trust-store steps need escalation. The
# guest has passwordless sudo. Set HTTP_BROKER_SUDO to empty to skip it, which
# is what the test suite does when writing into a temporary directory.
SUDO="${HTTP_BROKER_SUDO-sudo}"
if [[ $EUID -eq 0 ]]; then
	SUDO=""
fi

# The host, as seen from inside the Lima guest.
BROKER_HOST="${HTTP_BROKER_HOST:-host.lima.internal}"
PROXY_PORT="${HTTP_BROKER_PROXY_PORT:-8220}"

for required in "$TOKEN_FILE" "$CA_SOURCE"; do
	if [[ ! -r "$required" ]]; then
		cat >&2 <<EOF
error: missing $required

This file is created on the host by http-broker and must be copied into the
sandbox by sandbox-manager's copy_paths config:

  "copy_paths": [
    "~/.config/http-broker/auth-token",
    "~/.local/share/http-broker/ca.pem"
  ]

See the http-broker README for more information.
EOF
		exit 1
	fi
done

# --- Install the CA into the guest trust store ---------------------------
#
# Six environment variables follow below because no single one reaches every
# runtime. Installing the CA into the system store as well covers tools that
# consult neither.

echo "Installing the http-broker CA to $CA_DEST"
$SUDO install -D -m 0644 "$CA_SOURCE" "$CA_DEST"

if command -v "$UPDATE_CA" >/dev/null 2>&1; then
	$SUDO "$UPDATE_CA" >/dev/null
else
	echo "warning: $UPDATE_CA not found; the system trust store was not refreshed" >&2
fi

# --- Write the marker-fenced shell block ---------------------------------

MARKER_START="# >>> http-broker >>>"
MARKER_END="# <<< http-broker <<<"
BASHRC="$HOME/.bashrc"

touch "$BASHRC"

# Remove any previous block, then append the current one. Editing in place
# would drift as the block's contents change between versions.
if grep -qF "$MARKER_START" "$BASHRC"; then
	echo "Replacing the existing http-broker block in $BASHRC"
	sed -i "/^${MARKER_START}$/,/^${MARKER_END}$/d" "$BASHRC"
else
	echo "Adding the http-broker block to $BASHRC"
fi

cat >>"$BASHRC" <<EOF
$MARKER_START
# Managed by configure-http-broker.sh. Edits are lost on re-provision.

# The token is read at shell startup, so rotating it on the host and
# re-provisioning is enough — this file does not need regenerating.
export HTTP_BROKER_TOKEN="\$(cat "$TOKEN_FILE" 2>/dev/null)"

export HTTP_PROXY="http://x:\${HTTP_BROKER_TOKEN}@${BROKER_HOST}:${PROXY_PORT}"
export HTTPS_PROXY="\$HTTP_PROXY"
export http_proxy="\$HTTP_PROXY"
export https_proxy="\$HTTP_PROXY"

# Carve out the host itself. mcp-broker (:8200) and local-gomod-proxy (:7070)
# are reached over the same loopback forward, and routing them through this
# proxy would make it a single point of failure for both.
export NO_PROXY="${BROKER_HOST},localhost,127.0.0.1,::1"
export no_proxy="\$NO_PROXY"

# Six variables, because no single one reaches every runtime: SSL_CERT_FILE
# alone does not cover Node, Python requests, curl, git, or Deno.
export SSL_CERT_FILE="$CA_DEST"
export NODE_EXTRA_CA_CERTS="$CA_DEST"
export REQUESTS_CA_BUNDLE="$CA_DEST"
export CURL_CA_BUNDLE="$CA_DEST"
export GIT_SSL_CAINFO="$CA_DEST"
export DENO_CERT="$CA_DEST"
$MARKER_END
EOF

echo "Configured. Open a new shell, or run: source $BASHRC"
