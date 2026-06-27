#!/bin/bash
# Provisioning script for https://github.com/averycrespi/agent-tools/tree/main/sandbox-manager

set -euo pipefail

command_exists() { command -v "$1" &>/dev/null; }

cd ~/work/agent-tools

NODE_VERSION=$(awk '$1 == "nodejs" { print $2 }' .tool-versions)
if [[ -z "$NODE_VERSION" ]]; then
	echo "No nodejs version found in .tool-versions"
	exit 1
fi

echo "Installing asdf nodejs v$NODE_VERSION"
asdf install nodejs "$NODE_VERSION"

echo "Reshimming asdf nodejs"
asdf reshim nodejs

GO_VERSION=$(awk '$1 == "golang" { print $2 }' .tool-versions)
if [[ -z "$GO_VERSION" ]]; then
	echo "No golang version found in .tool-versions"
	exit 1
fi

echo "Installing asdf golang v$GO_VERSION"
asdf install golang "$GO_VERSION"

echo "Reshimming asdf golang"
asdf reshim golang

echo "Installing agent-tools dependencies"
make install-dev