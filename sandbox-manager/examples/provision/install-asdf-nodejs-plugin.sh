#!/bin/bash
# Install the asdf nodejs plugin.
# Requires asdf to already be installed — run asdf.sh first (or install it yourself).

set -euo pipefail

if ! command -v asdf &>/dev/null; then
	echo "error: asdf not found on PATH" >&2
	exit 1
fi

if ! asdf plugin list 2>/dev/null | grep -qx 'nodejs'; then
	echo "Adding asdf nodejs plugin"
	asdf plugin add nodejs
else
	echo "asdf nodejs plugin already installed"
fi
