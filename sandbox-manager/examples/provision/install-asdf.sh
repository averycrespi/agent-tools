#!/bin/bash
# Install asdf (version manager) as a prebuilt binary from GitHub releases.
#
# Re-running this script is safe: the install is skipped when asdf is already on
# PATH, and the ~/.bashrc block is replaced wholesale rather than left alone, so
# a change to its contents in a newer version of this script reaches an
# already-provisioned sandbox.

set -euo pipefail

ASDF_VERSION="v0.18.0"

command_exists() { command -v "$1" &>/dev/null; }

if ! command_exists curl; then
	echo "Installing curl (required to fetch asdf)"
	sudo apt-get update -qq
	sudo apt-get install -y -qq curl
else
	echo "curl already installed"
fi

if ! command_exists asdf; then
	ARCH="$(dpkg --print-architecture)"
	TARBALL="asdf-${ASDF_VERSION}-linux-${ARCH}.tar.gz"
	URL="https://github.com/asdf-vm/asdf/releases/download/${ASDF_VERSION}/${TARBALL}"

	echo "Installing asdf ${ASDF_VERSION} (${ARCH})"
	TMP="$(mktemp -d)"
	trap 'rm -rf "$TMP"' EXIT
	curl -fsSL "$URL" | tar -C "$TMP" -xz
	sudo install -m 0755 "$TMP/asdf" /usr/local/bin/asdf
else
	echo "asdf already installed"
fi

MARKER_START="# >>> asdf-config >>>"
MARKER_END="# <<< asdf-config <<<"
BASHRC="$HOME/.bashrc"

touch "$BASHRC"

# Remove any previous block, then append the current one. Editing in place
# would drift as the block's contents change between versions.
if grep -qF "$MARKER_START" "$BASHRC"; then
	echo "Replacing the existing asdf-config block in ~/.bashrc"
	sed -i "/^${MARKER_START}$/,/^${MARKER_END}$/d" "$BASHRC"
else
	echo "Adding asdf config to ~/.bashrc"
fi

# Keep the managed block separate when an existing file lacks a final newline.
if [[ -s "$BASHRC" && -n "$(tail -c 1 "$BASHRC")" ]]; then
	printf '\n' >>"$BASHRC"
fi

# Quoted heredoc: $HOME and $PATH must reach ~/.bashrc unexpanded so they
# resolve at shell startup. The markers are literal copies of the two above.
cat >>"$BASHRC" <<'EOF'
# >>> asdf-config >>>
# asdf version manager
export ASDF_DATA_DIR="$HOME/.asdf"
export PATH="$ASDF_DATA_DIR/shims:$PATH"
# <<< asdf-config <<<
EOF

echo "Configured. Open a new shell, or run: source $BASHRC"
