#!/bin/bash
# Install Docker Engine and enable the daemon.

set -euo pipefail

command_exists() { command -v "$1" &>/dev/null; }

if ! command_exists curl; then
    echo "Installing curl (required to fetch the Docker installer)"
    sudo apt-get update -qq
    sudo apt-get install -y -qq curl
fi

if ! command_exists docker; then
    echo "Installing Docker"
    curl -fsSL https://get.docker.com | sudo sh
    sudo usermod -aG docker "$USER"
else
    echo "Docker already installed, skipping"
fi

if ! systemctl is-active --quiet docker; then
    echo "Starting Docker"
    sudo systemctl enable --now docker
else
    echo "Docker already running, skipping"
fi
