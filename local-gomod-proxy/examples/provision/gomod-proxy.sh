#!/bin/bash
# Configure a Lima sandbox to route Go module resolution through the host's
# local-gomod-proxy over HTTPS + basic auth.
#
# Depends on the host running local-gomod-proxy (either manually via
# `local-gomod-proxy serve` or via the launchd agent; see docs/launchd.md).
# That process writes the TLS cert and credentials to
# $HOME/.local/state/local-gomod-proxy/{cert.pem,credentials} on the host.
#
# The sandbox needs both files. This script does NOT read them from a host
# mount — the sandbox-manager `copy_paths` config must ship them in. Example
# config entry (paired with the `scripts` entry that invokes this file):
#
#   "copy_paths": [
#     "~/.local/state/local-gomod-proxy/cert.pem",
#     "~/.local/state/local-gomod-proxy/credentials"
#   ]
#
# Paths starting with `~/` expand to the user home, so the files land at the
# same path inside the sandbox. sb provision re-runs copy_paths before the
# scripts, so cert rotation is picked up transparently on the next provision.
#
# This script:
#   (a) installs the proxy's self-signed cert into the sandbox's system
#       trust store via sudo update-ca-certificates (Lima sandboxes have
#       passwordless sudo).
#   (b) writes a marker-fenced GOPROXY block to ~/.bashrc.
# Both steps are idempotent.
#
# Rotation: if the host regenerates state (annual cert expiry, or manual via
# rm -rf $state_dir which refreshes both cert AND credentials), re-run
# `sb provision` to re-copy both files and refresh the trust store.

set -euo pipefail

command_exists() { command -v "$1" &>/dev/null; }

if ! command_exists go; then
	echo "error: go not found on PATH — install Go first (e.g. asdf-golang.sh)" >&2
	exit 1
fi

if ! command_exists update-ca-certificates; then
	echo "error: update-ca-certificates not found; this script targets Debian/Ubuntu sandboxes" >&2
	exit 1
fi

STATE_DIR="$HOME/.local/state/local-gomod-proxy"
CERT_FILE="$STATE_DIR/cert.pem"
CREDS_FILE="$STATE_DIR/credentials"
INSTALLED_CERT="/usr/local/share/ca-certificates/local-gomod-proxy.crt"

if [[ ! -r "$CERT_FILE" || ! -r "$CREDS_FILE" ]]; then
	cat >&2 <<EOF
error: missing $CERT_FILE and/or $CREDS_FILE.

These files are created on the host by local-gomod-proxy on first launch and
must be copied into the sandbox via sandbox-manager's copy_paths config.

See the local-gomod-proxy docs for more information.
EOF
	exit 1
fi

# File format is a single line "x:<token>\n". Strip the trailing newline.
CREDS="$(tr -d '\n' <"$CREDS_FILE")"
if [[ -z "$CREDS" || "$CREDS" != x:* ]]; then
	echo "error: $CREDS_FILE is malformed (expected 'x:<token>')" >&2
	exit 1
fi

# Install cert into the system trust store. update-ca-certificates picks up
# anything in /usr/local/share/ca-certificates/*.crt. Skip the rewrite +
# trust-store rebuild if the installed cert already matches byte-for-byte.
if ! [[ -f "$INSTALLED_CERT" ]] || ! sudo cmp -s "$CERT_FILE" "$INSTALLED_CERT"; then
	echo "Installing local-gomod-proxy cert into system trust store"
	sudo cp "$CERT_FILE" "$INSTALLED_CERT"
	sudo update-ca-certificates >/dev/null
else
	echo "local-gomod-proxy cert already in system trust store, skipping"
fi

MARKER_START="# >>> local-gomod-proxy >>>"
MARKER_END="# <<< local-gomod-proxy <<<"

if grep -qF "$MARKER_START" "$HOME/.bashrc" 2>/dev/null; then
	echo "local-gomod-proxy env already configured in ~/.bashrc, skipping"
	exit 0
fi

echo "Configuring GOPROXY in ~/.bashrc"
cat >>"$HOME/.bashrc" <<EOF

$MARKER_START
# Route Go module resolution through the host's local-gomod-proxy over HTTPS.
# The proxy's self-signed cert is installed into the sandbox's system trust
# store (see the install step in gomod-proxy.sh) so Go can verify it.
# Credentials were copied in from the host by sandbox-manager's copy_paths.
export GOPROXY="https://\$(tr -d '\n' < \$HOME/.local/state/local-gomod-proxy/credentials)@host.lima.internal:7070/"
# go.sum (committed to the repo) is the primary integrity check; disable the
# public checksum database so private modules don't leak to sum.golang.org.
export GOSUMDB=off
# Defense in depth: even if something re-sets GOPRIVATE via the environment,
# matching modules should still route through GOPROXY.
unset GOPRIVATE
$MARKER_END
EOF
