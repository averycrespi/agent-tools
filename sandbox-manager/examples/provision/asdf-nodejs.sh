#!/bin/bash
# Install the latest Node.js via asdf.
# Requires asdf to already be installed — run asdf.sh first (or install it yourself).

set -euo pipefail

if ! command -v asdf &>/dev/null; then
    echo "error: asdf not found on PATH" >&2
    exit 1
fi

export ASDF_DATA_DIR="${ASDF_DATA_DIR:-$HOME/.asdf}"
export PATH="$ASDF_DATA_DIR/shims:$PATH"

if ! asdf plugin list 2>/dev/null | grep -qx 'nodejs'; then
    echo "Adding asdf nodejs plugin"
    asdf plugin add nodejs
else
    echo "asdf nodejs plugin already installed, skipping"
fi

asdf install nodejs latest
asdf set --home nodejs latest
