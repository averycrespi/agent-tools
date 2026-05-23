#!/bin/bash
# Install asdf (version manager) as a prebuilt binary from GitHub releases.

set -euo pipefail

ASDF_VERSION="v0.18.0"

command_exists() { command -v "$1" &>/dev/null; }

if command_exists asdf; then
    echo "asdf already installed, skipping"
    exit 0
fi

if ! command_exists curl; then
    echo "Installing curl (required to fetch asdf)"
    sudo apt-get update -qq
    sudo apt-get install -y -qq curl
fi

ARCH="$(dpkg --print-architecture)"
TARBALL="asdf-${ASDF_VERSION}-linux-${ARCH}.tar.gz"
URL="https://github.com/asdf-vm/asdf/releases/download/${ASDF_VERSION}/${TARBALL}"

echo "Installing asdf ${ASDF_VERSION} (${ARCH})"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
curl -fsSL "$URL" | tar -C "$TMP" -xz
sudo install -m 0755 "$TMP/asdf" /usr/local/bin/asdf

if ! grep -q 'ASDF_DATA_DIR' "$HOME/.bashrc"; then
    cat >> "$HOME/.bashrc" <<'EOF'

# asdf version manager
export ASDF_DATA_DIR="$HOME/.asdf"
export PATH="$ASDF_DATA_DIR/shims:$PATH"
EOF
fi
