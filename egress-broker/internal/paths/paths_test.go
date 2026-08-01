package paths_test

import (
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/egress-broker/internal/paths"
)

func TestXDGOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/c")
	t.Setenv("XDG_DATA_HOME", "/d")

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"ConfigDir", paths.ConfigDir(), "/c/egress-broker"},
		{"DataDir", paths.DataDir(), "/d/egress-broker"},
		{"ConfigFile", paths.ConfigFile(), "/c/egress-broker/config.json"},
		{"RulesFile", paths.RulesFile(), "/c/egress-broker/rules.json"},
		{"TokenFile", paths.TokenFile(), "/c/egress-broker/auth-token"},
		{"AuditDB", paths.AuditDB(), "/d/egress-broker/audit.db"},
		{"CAKey", paths.CAKey(), "/d/egress-broker/ca.key"},
		{"CACert", paths.CACert(), "/d/egress-broker/ca.pem"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != filepath.FromSlash(tc.want) {
				t.Fatalf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestDefaultsUnderHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/tester")

	if got, want := paths.ConfigDir(), filepath.Join("/home/tester", ".config", "egress-broker"); got != want {
		t.Fatalf("ConfigDir() = %q, want %q", got, want)
	}
	if got, want := paths.DataDir(), filepath.Join("/home/tester", ".local", "share", "egress-broker"); got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}
