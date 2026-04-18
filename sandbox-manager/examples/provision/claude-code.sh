#!/bin/bash
# Install Claude Code (native binary).

set -euo pipefail

command_exists() { command -v "$1" &>/dev/null; }

if command_exists claude; then
    echo "Claude Code already installed, skipping"
    exit 0
fi

if ! command_exists curl; then
    echo "Installing curl (required to fetch the Claude Code installer)"
    sudo apt-get update -qq
    sudo apt-get install -y -qq curl
fi

echo "Installing Claude Code"
curl -fsSL https://claude.ai/install.sh | bash
