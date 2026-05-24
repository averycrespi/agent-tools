#!/bin/bash
# Install the asdf golang plugin.
# Requires asdf to already be installed — run install-asdf.sh first.

set -euo pipefail

if ! command -v asdf &>/dev/null; then
	echo "error: asdf not found on PATH" >&2
	exit 1
fi

if ! asdf plugin list 2>/dev/null | grep -qx 'golang'; then
	echo "Installing asdf golang plugin"
	asdf plugin add golang
else
	echo "asdf golang plugin already installed"
fi
