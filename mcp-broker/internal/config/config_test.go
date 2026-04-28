package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_CreatesDefaultOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 8200, cfg.Port)
	require.Equal(t, "info", cfg.Log.Level)
	require.FileExists(t, path)
}

func TestLoad_ReadsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"port": 9000}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 9000, cfg.Port)
}

func TestRefresh_BackfillsNewDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"port": 9000}`), 0o600)
	require.NoError(t, err)

	written, err := Refresh(path)
	require.NoError(t, err)
	require.Equal(t, path, written)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 9000, cfg.Port)
	require.Equal(t, "info", cfg.Log.Level)
}

func TestConfig_ServerTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": {
			"echo": {"command": "echo", "args": ["hello"]},
			"remote": {"type": "streamable-http", "url": "http://localhost:3000/mcp"}
		}
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 2)
	require.Equal(t, "echo", cfg.Servers["echo"].Command)
	require.Equal(t, "streamable-http", cfg.Servers["remote"].Type)
	require.Equal(t, "http://localhost:3000/mcp", cfg.Servers["remote"].URL)
}

func TestDefaultConfig_OpenBrowserDefaultsTrue(t *testing.T) {
	cfg := DefaultConfig()
	require.True(t, cfg.OpenBrowser)
}

func TestDefaultConfig_HostDefaultsToLoopback(t *testing.T) {
	cfg := DefaultConfig()
	require.Equal(t, "127.0.0.1", cfg.Host)
}

func TestLoad_HostBackfillsToLoopbackWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"port": 9000}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", cfg.Host)
}

func TestLoad_HostFromJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"host": "localhost"}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "localhost", cfg.Host)
}

func TestLoad_OpenBrowserFromJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"open_browser": false}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.False(t, cfg.OpenBrowser)
}

func TestConfigPath_ReturnsXDGPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path := ConfigPath()
	require.Equal(t, filepath.Join(dir, "mcp-broker", "config.json"), path)
}

func TestLoad_TelegramConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"approval_timeout_seconds": 300,
		"telegram": {
			"enabled": true,
			"token": "mytoken",
			"chat_id": "123456"
		}
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 300, cfg.ApprovalTimeoutSeconds)
	require.True(t, cfg.Telegram.Enabled)
	require.Equal(t, "mytoken", cfg.Telegram.Token)
	require.Equal(t, "123456", cfg.Telegram.ChatID)
}

func TestDefaultConfig_TelegramDisabledByDefault(t *testing.T) {
	cfg := DefaultConfig()
	require.False(t, cfg.Telegram.Enabled)
	require.Equal(t, 600, cfg.ApprovalTimeoutSeconds)
}

// TestRuleConfig_NoArgs verifies that a rule with no args field round-trips
// through Load/Save with the "args" key absent in the serialized JSON.
func TestRuleConfig_NoArgs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{"rules": [{"tool": "*", "verdict": "require-approval"}]}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Rules, 1)
	require.Nil(t, cfg.Rules[0].Args)

	// Save and re-read the raw JSON to confirm "args" key is absent.
	_, err = Save(cfg, path)
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	rules, ok := decoded["rules"].([]any)
	require.True(t, ok)
	require.Len(t, rules, 1)

	rule, ok := rules[0].(map[string]any)
	require.True(t, ok)
	_, hasArgs := rule["args"]
	require.False(t, hasArgs, "args key must be absent when rule has no args")
}

// TestRuleConfig_ExactArgRoundTrip verifies that a rule with one exact-string
// arg pattern round-trips through Load/Save and back unchanged in semantics.
func TestRuleConfig_ExactArgRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{"rules": [{"tool": "git_push", "verdict": "allow", "args": [{"path": "remote", "match": "origin"}]}]}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Rules, 1)

	rule := cfg.Rules[0]
	require.Equal(t, "git_push", rule.Tool)
	require.Equal(t, "allow", rule.Verdict)
	require.Len(t, rule.Args, 1)
	require.Equal(t, "remote", rule.Args[0].Path)
	require.Equal(t, json.RawMessage(`"origin"`), rule.Args[0].Match)

	// Save and reload — semantics must be preserved.
	_, err = Save(cfg, path)
	require.NoError(t, err)

	cfg2, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, cfg.Rules[0].Tool, cfg2.Rules[0].Tool)
	require.Equal(t, cfg.Rules[0].Verdict, cfg2.Rules[0].Verdict)
	require.Len(t, cfg2.Rules[0].Args, 1)
	require.Equal(t, "remote", cfg2.Rules[0].Args[0].Path)
	require.Equal(t, json.RawMessage(`"origin"`), cfg2.Rules[0].Args[0].Match)
}

// TestRuleConfig_MixedArgsRoundTrip verifies a rule with mixed exact + regex
// arg patterns round-trips correctly, keeping Match as raw JSON.
func TestRuleConfig_MixedArgsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
  "rules": [
    {
      "tool": "git_push",
      "verdict": "allow",
      "args": [
        {"path": "remote", "match": "origin"},
        {"path": "commit.message", "match": {"regex": "^feat:"}}
      ]
    }
  ]
}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Rules, 1)

	rule := cfg.Rules[0]
	require.Len(t, rule.Args, 2)

	// First pattern: exact string.
	require.Equal(t, "remote", rule.Args[0].Path)
	var exactVal string
	require.NoError(t, json.Unmarshal(rule.Args[0].Match, &exactVal))
	require.Equal(t, "origin", exactVal)

	// Second pattern: regex object — stays as RawMessage, not interpreted here.
	require.Equal(t, "commit.message", rule.Args[1].Path)
	var regexObj map[string]string
	require.NoError(t, json.Unmarshal(rule.Args[1].Match, &regexObj))
	require.Equal(t, "^feat:", regexObj["regex"])

	// Save and reload to confirm round-trip.
	_, err = Save(cfg, path)
	require.NoError(t, err)

	cfg2, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg2.Rules[0].Args, 2)

	// Regex RawMessage decodes to the same object after round-trip.
	var regexObj2 map[string]string
	require.NoError(t, json.Unmarshal(cfg2.Rules[0].Args[1].Match, &regexObj2))
	require.Equal(t, "^feat:", regexObj2["regex"])
}
