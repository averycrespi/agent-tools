#!/bin/bash
# Install the latest Go via asdf.
# Requires asdf to already be installed — run asdf.sh first (or install it yourself).

set -euo pipefail

if ! command -v asdf &>/dev/null; then
    echo "error: asdf not found on PATH" >&2
    exit 1
fi

export ASDF_DATA_DIR="${ASDF_DATA_DIR:-$HOME/.asdf}"
export PATH="$ASDF_DATA_DIR/shims:$PATH"

if ! asdf plugin list 2>/dev/null | grep -qx 'golang'; then
    echo "Adding asdf golang plugin"
    asdf plugin add golang
else
    echo "asdf golang plugin already installed, skipping"
fi

asdf install golang latest
asdf set --home golang latest
