// Package paths resolves the XDG locations http-broker reads and writes.
//
// Config-home files are operator-authored (config.json, rules.json) or
// secret (auth-token); data-home files are generated state (audit.db,
// ca.key, ca.pem). Keeping the split explicit means a user can delete the
// data home to reset state without losing their policy.
package paths

import (
	"os"
	"path/filepath"
)

// AppName is the directory name used under both XDG homes, and the
// go-keyring service name credentials are stored under.
const AppName = "http-broker"

func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func dataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// ConfigDir returns ~/.config/http-broker.
func ConfigDir() string { return filepath.Join(configHome(), AppName) }

// DataDir returns ~/.local/share/http-broker.
func DataDir() string { return filepath.Join(dataHome(), AppName) }

// ConfigFile returns the path to config.json.
func ConfigFile() string { return filepath.Join(ConfigDir(), "config.json") }

// RulesFile returns the default path to rules.json.
func RulesFile() string { return filepath.Join(ConfigDir(), "rules.json") }

// TokenFile returns the path to the dashboard bearer token.
func TokenFile() string { return filepath.Join(ConfigDir(), "auth-token") }

// AuditDB returns the path to the SQLite audit log.
func AuditDB() string { return filepath.Join(DataDir(), "audit.db") }

// CredentialIndex returns the path to the credential name index.
//
// It lives under the data home because it is derived state: the OS keychain
// cannot be enumerated, so the index records which names were stored. Deleting
// it loses no secret and is repaired by `http-broker credential get <name>`.
func CredentialIndex() string { return filepath.Join(DataDir(), "credentials.json") }

// CAKey returns the path to the MITM certificate authority private key.
func CAKey() string { return filepath.Join(DataDir(), "ca.key") }

// CACert returns the path to the MITM certificate authority certificate.
func CACert() string { return filepath.Join(DataDir(), "ca.pem") }
