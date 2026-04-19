package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const appName = "agent-gateway"

func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config")
}

func dataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share")
}

func ConfigDir() string      { return filepath.Join(configHome(), appName) }
func DataDir() string        { return filepath.Join(dataHome(), appName) }
func ConfigFile() string     { return filepath.Join(ConfigDir(), "config.hcl") }
func RulesDir() string       { return filepath.Join(ConfigDir(), "rules.d") }
func AdminTokenFile() string { return filepath.Join(ConfigDir(), "admin-token") }
func MasterKeyFile() string  { return filepath.Join(ConfigDir(), "master.key") }

// MasterKeyFileForID returns the path of the master-key file for a given key id.
// id 1 is the initial key; rotations bump the id. The legacy un-versioned
// MasterKeyFile() path is consulted only as a one-time migration source.
func MasterKeyFileForID(id int) string {
	return filepath.Join(ConfigDir(), fmt.Sprintf("master-key-%d", id))
}
func PIDFile() string { return filepath.Join(ConfigDir(), appName+".pid") }
func StateDB() string { return filepath.Join(DataDir(), "state.db") }
func CAKey() string   { return filepath.Join(DataDir(), "ca.key") }
func CACert() string  { return filepath.Join(DataDir(), "ca.pem") }
