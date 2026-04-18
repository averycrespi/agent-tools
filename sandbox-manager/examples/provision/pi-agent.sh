#!/bin/bash
# Install the Pi coding agent via npm.
# Requires npm to already be available on PATH — run asdf-nodejs.sh first (or install Node.js yourself).

set -euo pipefail

if ! command -v npm &>/dev/null; then
    echo "error: npm not found on PATH" >&2
    exit 1
fi

if command -v pi &>/dev/null; then
    echo "Pi agent already installed, skipping"
    exit 0
fi

echo "Installing Pi coding agent"
npm install -g @mariozechner/pi-coding-agent
