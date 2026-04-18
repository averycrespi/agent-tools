package secrets

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"encoding/hex"

	keyring "github.com/zalando/go-keyring"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

const (
	keychainService = "agent-gateway"
	keychainAccount = "master-key"
)

// Resolve resolves the master key using the keychain with a file fallback.
// Returns (key, fromFile, error). fromFile is true when the key came from
// or was written to the file fallback path.
//
// Resolution order:
//  1. Keychain (keychainService / keychainAccount).
//  2. File at paths.MasterKeyFile() (mode 0o600).
//  3. Generate a new key; try keychain first, fall back to file (warning).
func Resolve(logger *slog.Logger) ([]byte, bool, error) {
	return resolveWithPath(keychainService, keychainAccount, paths.MasterKeyFile(), logger)
}

// resolveKey is the internal helper used by NewStore.
func resolveKey(logger *slog.Logger) ([]byte, bool, error) {
	return Resolve(logger)
}

// persistKey writes the key to keychain, falling back to file.
func persistKey(key []byte, logger *slog.Logger) error {
	return persistWithPath(key, keychainService, keychainAccount, paths.MasterKeyFile(), logger)
}

// resolveWithPath is the testable variant. It accepts explicit service/account/path.
func resolveWithPath(service, account, filePath string, logger *slog.Logger) ([]byte, bool, error) {
	// 1. Try keychain.
	if hex, err := keyring.Get(service, account); err == nil {
		key, decErr := hexToKey(hex)
		if decErr == nil {
			return key, false, nil
		}
		logger.Warn("master-key: keychain value is malformed; falling back to file", "error", decErr)
	}

	// 2. Try file.
	if data, err := os.ReadFile(filePath); err == nil {
		key, decErr := hexToKey(string(data))
		if decErr == nil {
			return key, true, nil
		}
		logger.Warn("master-key: file value is malformed; generating new key", "error", decErr, "path", filePath)
	}

	// 3. Generate a new key.
	key, err := generateKey()
	if err != nil {
		return nil, false, fmt.Errorf("generate master key: %w", err)
	}

	fromFile := false
	if err := keyring.Set(service, account, keyToHex(key)); err != nil {
		// Keychain unavailable — fall back to file.
		logger.Warn("master-key: keychain unavailable, writing key to file; keep this file secure",
			"path", filePath, "error", err)
		if writeErr := writeKeyFile(key, filePath); writeErr != nil {
			return nil, false, fmt.Errorf("write master key to file: %w", writeErr)
		}
		fromFile = true
	}
	return key, fromFile, nil
}

// persistWithPath writes the key to keychain or file, whichever is available.
func persistWithPath(key []byte, service, account, filePath string, logger *slog.Logger) error {
	if err := keyring.Set(service, account, keyToHex(key)); err != nil {
		logger.Warn("master-key: keychain unavailable during persist, writing to file",
			"path", filePath, "error", err)
		return writeKeyFile(key, filePath)
	}
	return nil
}

// writeKeyFile writes key as a hex string to path at mode 0o600.
func writeKeyFile(key []byte, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	return os.WriteFile(path, []byte(keyToHex(key)), 0o600)
}

func keyToHex(key []byte) string {
	return hex.EncodeToString(key)
}

func hexToKey(s string) ([]byte, error) {
	key, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("key must be 32 bytes")
	}
	return key, nil
}

// ResolveTestKey resolves (or generates) a master key using only the file
// fallback path. Used in tests to avoid touching the real keychain.
func ResolveTestKey(filePath string, logger *slog.Logger) ([]byte, bool, error) {
	// Try to read existing file.
	if data, err := os.ReadFile(filePath); err == nil {
		key, decErr := hexToKey(string(data))
		if decErr == nil {
			return key, true, nil
		}
		logger.Warn("master-key: test file value is malformed; generating new key", "error", decErr)
	}

	// Generate and write.
	key, err := generateKey()
	if err != nil {
		return nil, false, err
	}
	if writeErr := writeKeyFile(key, filePath); writeErr != nil {
		return nil, false, fmt.Errorf("write key file: %w", writeErr)
	}
	return key, true, nil
}
