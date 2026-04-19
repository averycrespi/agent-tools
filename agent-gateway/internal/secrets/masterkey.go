package secrets

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	keyring "github.com/zalando/go-keyring"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/atomicfile"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

const (
	keychainService = "agent-gateway"

	// legacyKeychainAccount and legacyMasterKeyFile name the unversioned
	// master-key location used before the meta(active_key_id) scheme. They
	// are consulted only as a one-time migration source for id=1.
	legacyKeychainAccount = "master-key"
)

// keychainAccountForID returns the keychain account name holding the master
// key for the given id (1-based, monotonically increasing across rotations).
func keychainAccountForID(id int) string {
	return fmt.Sprintf("master-key-%d", id)
}

// ResolveID resolves the master key for a given key id, with an OS keychain
// lookup and a file-fallback in $XDG_CONFIG_HOME/agent-gateway/master-key-<id>.
// If id == 1 and no key is found at the new locations, ResolveID consults the
// legacy (unversioned) keychain account and master.key file; if either is
// present its contents are migrated to the id=1 location (and the legacy
// entries removed best-effort).
//
// On a fresh install (no key found anywhere) a new key is generated, persisted,
// and returned. fromFile is true when the resolved key came from (or was
// written to) the file fallback rather than the OS keychain.
func ResolveID(id int, logger *slog.Logger) ([]byte, bool, error) {
	if id < 1 {
		return nil, false, fmt.Errorf("master-key: invalid id %d", id)
	}
	return resolveIDWithPaths(keychainService, keychainAccountForID(id), paths.MasterKeyFileForID(id),
		legacyKeychainAccount, paths.MasterKeyFile(), id == 1, logger)
}

// PersistID writes key under the given id to the OS keychain, falling back to
// a file at paths.MasterKeyFileForID(id) when the keychain is unavailable.
func PersistID(key []byte, id int, logger *slog.Logger) error {
	if id < 1 {
		return fmt.Errorf("master-key: invalid id %d", id)
	}
	return persistWithPath(key, keychainService, keychainAccountForID(id), paths.MasterKeyFileForID(id), logger)
}

// DeleteID removes the keychain entry and file for the given id, best-effort.
// Errors are logged at warn level but not returned: an orphaned key on disk is
// not a correctness issue and we never want rotation cleanup to fail the
// caller after a successful commit.
func DeleteID(id int, logger *slog.Logger) {
	if id < 1 {
		return
	}
	if err := keyring.Delete(keychainService, keychainAccountForID(id)); err != nil &&
		!errors.Is(err, keyring.ErrNotFound) {
		logger.Warn("master-key: failed to delete keychain entry", "id", id, "error", err)
	}
	path := paths.MasterKeyFileForID(id)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("master-key: failed to delete key file", "path", path, "error", err)
	}
}

// resolveIDWithPaths is the testable variant. It accepts explicit
// service/account/file path for the active id and (optionally) the legacy
// account/file to migrate from when tryLegacy is true.
func resolveIDWithPaths(
	service, account, filePath string,
	legacyAccount, legacyFilePath string,
	tryLegacy bool,
	logger *slog.Logger,
) ([]byte, bool, error) {
	// 1. Try keychain at the active account.
	if hexStr, err := keyring.Get(service, account); err == nil {
		key, decErr := hexToKey(hexStr)
		if decErr == nil {
			return key, false, nil
		}
		logger.Warn("master-key: keychain value is malformed; falling back to file",
			"account", account, "error", decErr)
	}

	// 2. Try file at the active path.
	if data, err := os.ReadFile(filePath); err == nil {
		key, decErr := hexToKey(string(data))
		if decErr == nil {
			return key, true, nil
		}
		logger.Warn("master-key: file value is malformed; trying legacy",
			"path", filePath, "error", decErr)
	}

	// 3. Legacy migration (id=1 only): try the unversioned locations and
	// promote whatever we find to the new (id=1) location.
	if tryLegacy {
		if key, fromFile, ok := migrateFromLegacy(service, account, filePath,
			legacyAccount, legacyFilePath, logger); ok {
			return key, fromFile, nil
		}
	}

	// 4. Generate a new key and persist it.
	key, err := generateKey()
	if err != nil {
		return nil, false, fmt.Errorf("generate master key: %w", err)
	}
	fromFile := false
	if err := keyring.Set(service, account, keyToHex(key)); err != nil {
		logger.Warn("master-key: keychain unavailable, writing key to file; keep this file secure",
			"path", filePath, "error", err)
		if writeErr := writeKeyFile(key, filePath); writeErr != nil {
			return nil, false, fmt.Errorf("write master key to file: %w", writeErr)
		}
		fromFile = true
	}
	return key, fromFile, nil
}

// migrateFromLegacy looks for a master key at the unversioned legacy location
// (used before the meta(active_key_id) scheme) and, if found, promotes it to
// the active (id=1) location, then removes the legacy entries best-effort.
// Returns ok=false when no legacy key was found.
func migrateFromLegacy(
	service, account, filePath string,
	legacyAccount, legacyFilePath string,
	logger *slog.Logger,
) (key []byte, fromFile bool, ok bool) {
	// Legacy keychain.
	if hexStr, err := keyring.Get(service, legacyAccount); err == nil {
		k, decErr := hexToKey(hexStr)
		if decErr != nil {
			logger.Warn("master-key: legacy keychain value malformed; ignoring",
				"account", legacyAccount, "error", decErr)
		} else {
			if setErr := keyring.Set(service, account, hexStr); setErr != nil {
				logger.Warn("master-key: legacy migration: keychain unavailable, writing to file",
					"path", filePath, "error", setErr)
				if writeErr := writeKeyFile(k, filePath); writeErr != nil {
					return nil, false, false
				}
				_ = keyring.Delete(service, legacyAccount)
				logger.Info("master-key: migrated from legacy keychain to file", "path", filePath)
				return k, true, true
			}
			_ = keyring.Delete(service, legacyAccount)
			logger.Info("master-key: migrated from legacy keychain account",
				"from", legacyAccount, "to", account)
			return k, false, true
		}
	}

	// Legacy file.
	if data, err := os.ReadFile(legacyFilePath); err == nil {
		k, decErr := hexToKey(string(data))
		if decErr != nil {
			logger.Warn("master-key: legacy file value malformed; ignoring",
				"path", legacyFilePath, "error", decErr)
			return nil, false, false
		}
		if setErr := keyring.Set(service, account, keyToHex(k)); setErr != nil {
			if writeErr := writeKeyFile(k, filePath); writeErr != nil {
				return nil, false, false
			}
			_ = os.Remove(legacyFilePath)
			logger.Info("master-key: migrated legacy file to versioned file",
				"from", legacyFilePath, "to", filePath)
			return k, true, true
		}
		_ = os.Remove(legacyFilePath)
		logger.Info("master-key: migrated legacy file to keychain",
			"from", legacyFilePath, "to_account", account)
		return k, false, true
	}

	return nil, false, false
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

// writeKeyFile writes key as a hex string to path at mode 0o600, atomically.
func writeKeyFile(key []byte, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	return atomicfile.Write(path, []byte(keyToHex(key)), 0o600)
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
