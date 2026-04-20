#!/bin/bash
# Configure a Lima sandbox to route Go module resolution through the host's
# local-gomod-proxy at host.lima.internal:7070.
#
# Idempotent: writes a marker-fenced block to ~/.bashrc once and exits early
# on re-runs. Depends on Go being installed first (e.g. via sandbox-manager's
# asdf-golang.sh) so we can clear any persisted `go env -w GOPRIVATE=…`.
#
# Pairs with the host side — run `local-gomod-proxy serve` manually or install
# as a launchd agent (see local-gomod-proxy/docs/launchd.md).

set -euo pipefail

command_exists() { command -v "$1" &>/dev/null; }

if ! command_exists go; then
    echo "error: go not found on PATH — install Go first (e.g. asdf-golang.sh)" >&2
    exit 1
fi

MARKER_START="# >>> local-gomod-proxy >>>"
MARKER_END="# <<< local-gomod-proxy <<<"

# Clear any persisted `go env -w GOPRIVATE=…`. If GOPRIVATE is set, matching
# modules bypass GOPROXY and try to clone directly — which fails because the
# sandbox has no git credentials. The `unset` in ~/.bashrc below is the
# defense-in-depth partner to this persistent clear.
go env -u GOPRIVATE

if grep -qF "$MARKER_START" "$HOME/.bashrc" 2>/dev/null; then
    echo "local-gomod-proxy env already configured in ~/.bashrc, skipping"
    exit 0
fi

echo "Configuring GOPROXY in ~/.bashrc"
cat >> "$HOME/.bashrc" <<EOF

$MARKER_START
# Route Go module resolution through the host's local-gomod-proxy.
# Host binds 127.0.0.1:7070; Lima forwards host.lima.internal to it.
export GOPROXY=http://host.lima.internal:7070/
# go.sum (committed to the repo) is the primary integrity check; disable the
# public checksum database so private modules don't leak to sum.golang.org.
export GOSUMDB=off
# Defense in depth: even if something re-sets GOPRIVATE via the environment,
# matching modules should still route through GOPROXY.
unset GOPRIVATE
$MARKER_END
EOF
