// Package state loads or generates the TLS cert, private key, and basic-auth
// credentials that the proxy needs at startup. All files live under a single
// per-install state directory (default: $XDG_STATE_HOME/local-gomod-proxy).
package state

import (
	"fmt"
	"os"
	"path/filepath"
)

const dirName = "local-gomod-proxy"

// ResolveDir returns the absolute path to the proxy's state directory.
// Precedence: explicit override > $XDG_STATE_HOME/local-gomod-proxy >
// $HOME/.local/state/local-gomod-proxy.
func ResolveDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, dirName), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("cannot resolve state dir: XDG_STATE_HOME and HOME both unset")
	}
	return filepath.Join(home, ".local/state", dirName), nil
}

// EnsureDir creates dir with mode 0700 if it does not already exist.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating state dir %s: %w", dir, err)
	}
	// MkdirAll does not chmod if the dir already existed with looser perms.
	// Tighten it defensively — these files are secrets.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod state dir %s: %w", dir, err)
	}
	return nil
}
