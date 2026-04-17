package paths_test

import (
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func TestConfigDir_XDGOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got := paths.ConfigDir()
	want := filepath.Join("/tmp/xdg", "agent-gateway")
	if got != want {
		t.Fatalf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestDataDir_XDGOverride(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-data")
	got := paths.DataDir()
	want := filepath.Join("/tmp/xdg-data", "agent-gateway")
	if got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

func TestNamedPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/c")
	t.Setenv("XDG_DATA_HOME", "/d")
	cases := []struct{ got, want string }{
		{paths.ConfigFile(), "/c/agent-gateway/config.hcl"},
		{paths.RulesDir(), "/c/agent-gateway/rules.d"},
		{paths.AdminTokenFile(), "/c/agent-gateway/admin-token"},
		{paths.MasterKeyFile(), "/c/agent-gateway/master.key"},
		{paths.PIDFile(), "/c/agent-gateway/agent-gateway.pid"},
		{paths.StateDB(), "/d/agent-gateway/state.db"},
		{paths.CAKey(), "/d/agent-gateway/ca.key"},
		{paths.CACert(), "/d/agent-gateway/ca.pem"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}
