package config_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/egress-broker/internal/config"
)

func TestLoopbackValidation(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"ipv4 loopback", "127.0.0.1", false},
		{"ipv4 loopback high", "127.0.0.53", false},
		{"ipv6 loopback", "::1", false},
		{"ipv6 loopback bracketed", "[::1]", false},
		{"localhost", "localhost", false},
		{"localhost mixed case", "LocalHost", false},

		{"wildcard v4", "0.0.0.0", true},
		{"wildcard v6", "::", true},
		{"lan address", "192.168.1.10", true},
		{"public address", "93.184.216.34", true},
		{"link local", "169.254.169.254", true},
		{"hostname", "example.com", true},
		{"empty", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateLoopback(config.ListenerConfig{Host: tc.host, Port: 8220})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateLoopback(%q) = nil, want an error", tc.host)
				}
				if !errors.Is(err, config.ErrNonLoopback) {
					t.Fatalf("ValidateLoopback(%q) error %v, want it to wrap ErrNonLoopback", tc.host, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateLoopback(%q) = %v, want nil", tc.host, err)
			}
		})
	}
}

// TestLoopbackRejectedAtLoad proves the named error reaches the caller through
// Load, not just through the helper (AC-1).
func TestLoopbackRejectedAtLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	write(t, path, `{"proxy": {"host": "0.0.0.0", "port": 8220}}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load with a non-loopback proxy host = nil, want an error")
	}
	if !errors.Is(err, config.ErrNonLoopback) {
		t.Fatalf("error %v, want it to wrap ErrNonLoopback", err)
	}
	if !strings.Contains(err.Error(), "proxy listener") {
		t.Errorf("error %q should name which listener failed", err)
	}
}

func TestLoopbackRejectsSharedPort(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Dashboard.Port = cfg.Proxy.Port
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("Validate with proxy and dashboard on one port = nil, want an error")
	}
	if !strings.Contains(err.Error(), "reachable through the proxy") {
		t.Errorf("error %q should explain why sharing a port is unsafe", err)
	}
}

func TestRefreshBackfillsWithoutDiscardingOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// A sparse file: one override, everything else absent.
	write(t, path, `{"proxy": {"host": "127.0.0.1", "port": 9999}}`)

	if _, err := config.Refresh(path); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load after Refresh: %v", err)
	}

	if cfg.Proxy.Port != 9999 {
		t.Errorf("Proxy.Port = %d, want the override 9999 preserved", cfg.Proxy.Port)
	}
	if cfg.Dashboard.Port != config.DefaultDashboardPort {
		t.Errorf("Dashboard.Port = %d, want default %d backfilled", cfg.Dashboard.Port, config.DefaultDashboardPort)
	}
	if cfg.Audit.RetentionDays != config.DefaultRetentionDays {
		t.Errorf("Audit.RetentionDays = %d, want default %d backfilled", cfg.Audit.RetentionDays, config.DefaultRetentionDays)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want default \"info\" backfilled", cfg.Log.Level)
	}
	if cfg.RulesPath != filepath.Join(dir, "rules.json") {
		t.Errorf("RulesPath = %q, want it defaulted alongside the config file", cfg.RulesPath)
	}

	// Refresh must be idempotent: a second pass changes nothing.
	before := read(t, path)
	if _, err := config.Refresh(path); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if after := read(t, path); after != before {
		t.Errorf("Refresh is not idempotent:\nfirst:  %s\nsecond: %s", before, after)
	}
}

func TestLoadCreatesDefaultWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Proxy.Port != config.DefaultProxyPort || cfg.Dashboard.Port != config.DefaultDashboardPort {
		t.Errorf("default ports = %d/%d, want %d/%d",
			cfg.Proxy.Port, cfg.Dashboard.Port, config.DefaultProxyPort, config.DefaultDashboardPort)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Load should have written the default config file: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file mode = %v, want 0600", perm)
	}
}

func TestLoadParseErrorNamesFileAndOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	write(t, path, "{\n  \"proxy\": {\n    \"port\": ,\n  }\n}")

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load on malformed JSON = nil, want an error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q should name the file", err)
	}
	if !strings.Contains(err.Error(), "byte offset") {
		t.Errorf("error %q should name a byte offset", err)
	}
}

// TestLoadRulesFileParseErrorNamesFileAndOffset is the file-level half of
// AC-6. The per-rule half lives in internal/rules; loading orchestration lives
// here, mirroring the mcp-broker package split.
func TestLoadRulesFileParseErrorNamesFileAndOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	write(t, path, "{\n  \"fallthrough\": \"tunnel\",\n  \"rules\": [\n    {\"name\": }\n  ]\n}")

	_, err := config.LoadRulesFile(path)
	if err == nil {
		t.Fatal("LoadRulesFile on malformed JSON = nil, want an error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q should name the file", err)
	}
	if !strings.Contains(err.Error(), "byte offset") {
		t.Errorf("error %q should name a byte offset", err)
	}
}

// TestLoadRulesEngineNamesFileAndRule proves a per-rule failure surfaced
// through the loader still carries both the file and the rule name.
func TestLoadRulesEngineNamesFileAndRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	write(t, path, `{"fallthrough":"tunnel","rules":[{"name":"too-broad","host":"*.com","mode":"tunnel"}]}`)

	_, err := config.LoadRulesEngine(path)
	if err == nil {
		t.Fatal("LoadRulesEngine on an invalid rule = nil, want an error")
	}
	for _, want := range []string{path, "too-broad", "public suffix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestLoadRulesForConfigGeneratesDefault(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.json")

	cfg, err := config.Load(configFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	path, doc, err := config.LoadRulesForConfig(configFile, cfg)
	if err != nil {
		t.Fatalf("LoadRulesForConfig: %v", err)
	}
	if path != filepath.Join(dir, "rules.json") {
		t.Errorf("rules path = %q, want it alongside the config file", path)
	}
	// A fresh install must ship "tunnel": "deny" with no rules leaves a new
	// sandbox with no working network at all.
	if doc.Fallthrough != "tunnel" {
		t.Errorf("generated fallthrough = %q, want \"tunnel\"", doc.Fallthrough)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the default rules file should have been written: %v", err)
	}
}

func TestEnvCredentialsRequireHosts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.EnvCredentials = map[string]config.EnvCredential{
		"gh_bot": {Var: "GH_TOKEN"},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("an env credential with no bound hosts should be rejected")
	}
	if !strings.Contains(err.Error(), "gh_bot") {
		t.Errorf("error %q should name the credential", err)
	}

	cfg.EnvCredentials["gh_bot"] = config.EnvCredential{Var: "GH_TOKEN", Hosts: []string{"api.github.com"}}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("a bound env credential should validate: %v", err)
	}

	cfg.EnvCredentials["gh_bot"] = config.EnvCredential{Hosts: []string{"api.github.com"}}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("an env credential with no var should be rejected")
	}
}

func TestListenerAddr(t *testing.T) {
	cases := []struct{ host, want string }{
		{"127.0.0.1", "127.0.0.1:8220"},
		{"::1", "[::1]:8220"},
	}
	for _, tc := range cases {
		got := config.ListenerConfig{Host: tc.host, Port: 8220}.Addr()
		if got != tc.want {
			t.Errorf("Addr() for %q = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestSavedConfigRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := config.DefaultConfigAt(path)
	cfg.EnvCredentials = map[string]config.EnvCredential{
		"gh_bot": {Var: "GH_TOKEN", Hosts: []string{"api.github.com"}},
	}
	if _, err := config.Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want, _ := json.Marshal(cfg)
	got, _ := json.Marshal(loaded)
	if string(got) != string(want) {
		t.Errorf("round trip changed the config:\nsaved:  %s\nloaded: %s", want, got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestEnvCredentialsRejectPublicSuffix proves the check runs at config load,
// not only when a request first resolves the credential.
func TestEnvCredentialsRejectPublicSuffix(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.EnvCredentials = map[string]config.EnvCredential{
		"too_broad": {Var: "TOK", Hosts: []string{"*.com"}},
	}

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("an env credential bound to a public suffix should be rejected at load")
	}
	for _, want := range []string{"too_broad", "public suffix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}

	cfg.EnvCredentials["too_broad"] = config.EnvCredential{Var: "TOK", Hosts: []string{"api.github.com"}}
	if err := config.Validate(cfg); err != nil {
		t.Errorf("a normally bound env credential should validate: %v", err)
	}
}
