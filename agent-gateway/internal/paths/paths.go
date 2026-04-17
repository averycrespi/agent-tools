package paths

import (
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
func PIDFile() string        { return filepath.Join(ConfigDir(), appName+".pid") }
func StateDB() string        { return filepath.Join(DataDir(), "state.db") }
func CAKey() string          { return filepath.Join(DataDir(), "ca.key") }
func CACert() string         { return filepath.Join(DataDir(), "ca.pem") }
