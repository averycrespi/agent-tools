package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/config"
)

const CookieName = "po-auth"

func TokenPath() string {
	cfg, err := config.Load()
	if err != nil || cfg.ConfigDir == "" {
		return filepath.Join(defaultConfigDir(), "auth-token")
	}
	return filepath.Join(cfg.ConfigDir, "auth-token")
}

func EnsureToken(path string) (string, error) {
	if err := validateExistingTokenFile(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path) //nolint:gosec
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return writeNewToken(path)
}

func LoadToken(path string) (string, error) {
	if err := validateExistingTokenFile(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path) //nolint:gosec
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

func validateExistingTokenFile(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("token file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("token file permissions must not allow group or other access")
	}
	return nil
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

func defaultConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "po")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "po")
}
