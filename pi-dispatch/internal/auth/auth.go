package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
)

func TokenPath() string {
	return filepath.Join(config.ConfigDir(), "auth-token")
}

func EnsureToken(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // token path is controlled by XDG/user home.
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return writeNewToken(path)
}

func LoadToken(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // token path is controlled by XDG/user home.
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return string(data), nil
}

func RotateToken(path string) (string, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("removing token file: %w", err)
	}
	return writeNewToken(path)
}

func writeNewToken(path string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("creating token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("writing token file: %w", err)
	}
	return token, nil
}
