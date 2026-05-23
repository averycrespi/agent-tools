#!/bin/bash
# Install a list of apt packages idempotently.
# Edit the PACKAGES list below to match what you need.

set -euo pipefail

# ---- Edit this list ------------------------------------------------------
PACKAGES=(
    # git
    # vim
    # make
    # gcc
)
# --------------------------------------------------------------------------

if [ ${#PACKAGES[@]} -eq 0 ]; then
    echo "No packages configured. Edit PACKAGES in this script."
    exit 0
fi

MISSING=()
for pkg in "${PACKAGES[@]}"; do
    if ! dpkg -s "$pkg" &>/dev/null; then
        MISSING+=("$pkg")
    fi
done

if [ ${#MISSING[@]} -eq 0 ]; then
    echo "All apt packages already installed, skipping"
    exit 0
fi

echo "Installing apt packages: ${MISSING[*]}"
sudo apt-get update -qq
sudo apt-get install -y -qq "${MISSING[@]}"
