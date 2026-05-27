#!/bin/bash
# Install the latest Node.js via asdf.
# Requires asdf to already be installed — run install-asdf.sh first.

set -euo pipefail

if ! command -v asdf &>/dev/null; then
	echo "error: asdf not found on PATH" >&2
	exit 1
fi

if ! asdf plugin list 2>/dev/null | grep -qx 'nodejs'; then
	echo "Installing asdf nodejs plugin"
	asdf plugin add nodejs
else
	echo "asdf nodejs plugin already installed"
fi

echo "Installing latest nodejs version"
asdf install nodejs latest
asdf set --home nodejs latest
