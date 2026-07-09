package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadCreatesDefaultConfigAndRulesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "rules.json"), cfg.RulesPath)
	require.FileExists(t, path)

	rulesResult, err := LoadRulesForConfig(path, cfg)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "rules.json"), rulesResult.Path)
	require.Equal(t, DefaultRules(), rulesResult.Rules)
	require.FileExists(t, rulesResult.Path)

	rawConfig, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(rawConfig), `"rules_path"`)
	require.NotContains(t, string(rawConfig), `"rules":`)

	rawRules, err := os.ReadFile(rulesResult.Path)
	require.NoError(t, err)
	require.Contains(t, string(rawRules), `"rules":`)

	info, err := os.Stat(rulesResult.Path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestRulesPathDefaultsBesideEffectiveConfigPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile", "custom-config.json")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "profile", "rules.json"), cfg.RulesPath)

	result, err := LoadRulesForConfig(path, cfg)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "profile", "rules.json"), result.Path)
}

func TestLoadRulesForConfigMigratesLegacyEmbeddedRulesWhenRulesFileMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacyRules := []RuleConfig{{Tool: "github.*", Verdict: "allow", Reason: "safe"}}

	require.NoError(t, os.WriteFile(path, []byte(`{
		"rules_path": "`+filepath.Join(dir, "rules.json")+`",
		"rules": [{"tool": "github.*", "verdict": "allow", "reason": "safe"}]
	}`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	result, err := LoadRulesForConfig(path, cfg)
	require.NoError(t, err)
	require.True(t, result.MigratedLegacy)
	require.Equal(t, legacyRules, result.Rules)

	rawRules, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	require.Contains(t, string(rawRules), `"rules":`)
	require.Contains(t, string(rawRules), `"github.*"`)
}

func TestLoadRulesForConfigUsesRulesFileWhenLegacyRulesAlsoExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	rulesPath := filepath.Join(dir, "rules.json")

	require.NoError(t, os.WriteFile(path, []byte(`{
		"rules_path": "`+rulesPath+`",
		"rules": {"tool": "*", "verdict": "allow"}
	}`), 0o600))
	require.NoError(t, SaveRulesFile(rulesPath, []RuleConfig{{Tool: "slack.*", Verdict: "deny", Reason: "external wins"}}))

	cfg, err := Load(path)
	require.NoError(t, err)
	result, err := LoadRulesForConfig(path, cfg)
	require.NoError(t, err)
	require.True(t, result.IgnoredLegacy)
	require.False(t, result.MigratedLegacy)
	require.Equal(t, []RuleConfig{{Tool: "slack.*", Verdict: "deny", Reason: "external wins"}}, result.Rules)
}

func TestLoadRulesFileRejectsInvalidRulesDocuments(t *testing.T) {
	for name, body := range map[string]string{
		"invalid json":  `{"rules": [`,
		"missing rules": `{"metadata": true}`,
		"null rules":    `{"rules": null}`,
		"object rules":  `{"rules": {"tool": "*", "verdict": "allow"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rules.json")
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

			_, err := LoadRulesFile(path)
			require.Error(t, err)
		})
	}
}

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
	require.FileExists(t, filepath.Join(dir, "rules.json"))
}

func TestRefresh_MigratesLegacyRulesBeforeRemovingEmbeddedRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"port": 9000,
		"rules": [{"tool": "github.*", "verdict": "allow", "reason": "legacy"}]
	}`), 0o600))

	_, err := Refresh(path)
	require.NoError(t, err)

	rules, err := LoadRulesFile(filepath.Join(dir, "rules.json"))
	require.NoError(t, err)
	require.Equal(t, []RuleConfig{{Tool: "github.*", Verdict: "allow", Reason: "legacy"}}, rules)

	rawConfig, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(rawConfig), `"rules":`)
	require.Contains(t, string(rawConfig), `"rules_path"`)
}

func TestRefresh_PreservesExistingRulesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	rulesPath := filepath.Join(dir, "rules.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"port": 9000,
		"rules_path": "`+rulesPath+`",
		"rules": [{"tool": "github.*", "verdict": "allow", "reason": "legacy"}]
	}`), 0o600))
	require.NoError(t, SaveRulesFile(rulesPath, []RuleConfig{{Tool: "slack.*", Verdict: "deny", Reason: "external"}}))
	before, err := os.ReadFile(rulesPath)
	require.NoError(t, err)

	_, err = Refresh(path)
	require.NoError(t, err)

	after, err := os.ReadFile(rulesPath)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	rules, err := LoadRulesFile(rulesPath)
	require.NoError(t, err)
	require.Equal(t, []RuleConfig{{Tool: "slack.*", Verdict: "deny", Reason: "external"}}, rules)
}

func TestDefaultConfig_GrantsDefaults(t *testing.T) {
	cfg := DefaultConfig()
	require.Contains(t, cfg.Grants.Path, filepath.Join("mcp-broker", "grants.db"))
	require.EqualValues(t, 7*24*60*60, cfg.Grants.MaxTTLSeconds)
}

func TestLoad_RejectsInvalidGrantMaxTTL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"grants":{"max_ttl_seconds":0}}`), 0o600)
	require.NoError(t, err)

	_, err = Load(path)
	require.ErrorContains(t, err, "grants.max_ttl_seconds must be positive")
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

func TestLoad_MaxRequestBodyBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{"max_request_body_bytes": 1024}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.EqualValues(t, 1024, cfg.MaxRequestBodyBytes)
}

func TestDefaultConfig_MaxRequestBodyBytesDefaultsToTenMiB(t *testing.T) {
	cfg := DefaultConfig()
	require.EqualValues(t, 10*1024*1024, cfg.MaxRequestBodyBytes)
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

func TestConfig_ToolPatchesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"tool_patches": [
			{
				"tool": "github.search_*",
				"annotations": {
					"title": "GitHub search",
					"readOnlyHint": true,
					"destructiveHint": false,
					"idempotentHint": true,
					"openWorldHint": true
				}
			},
			{"tool": "github.delete_*", "disabled": true}
		]
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.ToolPatches, 2)

	patch := cfg.ToolPatches[0]
	require.Equal(t, "github.search_*", patch.Tool)
	require.NotNil(t, patch.Annotations)
	require.Equal(t, "GitHub search", *patch.Annotations.Title)
	require.True(t, *patch.Annotations.ReadOnlyHint)
	require.False(t, *patch.Annotations.DestructiveHint)
	require.True(t, *patch.Annotations.IdempotentHint)
	require.True(t, *patch.Annotations.OpenWorldHint)
	require.True(t, cfg.ToolPatches[1].Disabled)

	_, err = Save(cfg, path)
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"readOnlyHint": true`)
	require.Contains(t, string(raw), `"destructiveHint": false`)
	require.NotContains(t, string(raw), "ReadOnlyHint")
	require.NotContains(t, string(raw), "DestructiveHint")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	patches, ok := decoded["tool_patches"].([]any)
	require.True(t, ok)
	disabledPatch, ok := patches[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, disabledPatch["disabled"])
	_, hasLegacyDisable := disabledPatch["disable"]
	require.False(t, hasLegacyDisable)

	cfg2, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg2.ToolPatches, 2)
	require.False(t, *cfg2.ToolPatches[0].Annotations.DestructiveHint)
}

func TestConfig_ToolPatchOmittedAnnotations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"tool_patches": [{"tool": "github.delete_*", "disabled": true}]}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.ToolPatches, 1)
	require.Nil(t, cfg.ToolPatches[0].Annotations)

	_, err = Save(cfg, path)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	patches, ok := decoded["tool_patches"].([]any)
	require.True(t, ok)
	patch, ok := patches[0].(map[string]any)
	require.True(t, ok)
	_, hasAnnotations := patch["annotations"]
	require.False(t, hasAnnotations)
}

func TestConfig_ToolPatchLegacyDisableLoadsAsDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"tool_patches": [{"tool": "github.delete_*", "disable": true}]}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.ToolPatches, 1)
	require.True(t, cfg.ToolPatches[0].Disabled)

	_, err = Save(cfg, path)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	patches, ok := decoded["tool_patches"].([]any)
	require.True(t, ok)
	patch, ok := patches[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, patch["disabled"])
	_, hasLegacyDisable := patch["disable"]
	require.False(t, hasLegacyDisable)
}

func TestConfigPath_ReturnsXDGPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path := ConfigPath()
	require.Equal(t, filepath.Join(dir, "mcp-broker", "config.json"), path)
}

func TestLoad_ServerOAuthConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": {
			"remote": {
				"type": "streamable-http",
				"url": "https://example.com/mcp",
				"oauth": {
					"client_id": "test-client-id",
					"callback_port": 3118,
					"scopes": ["example:read"],
					"auth_server_metadata_url": "https://example.com/.well-known/oauth-authorization-server"
				}
			}
		}
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	oauth := cfg.Servers["remote"].OAuth
	require.NotNil(t, oauth)
	require.Equal(t, "test-client-id", oauth.ClientID)
	require.Equal(t, 3118, oauth.CallbackPort)
	require.Equal(t, []string{"example:read"}, oauth.Scopes)
	require.Equal(t, "https://example.com/.well-known/oauth-authorization-server", oauth.AuthServerMetadataURL)
}

func TestLoad_ServerOAuthConfigRejectsInvalidCallbackPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{"servers":{"bad":{"oauth":{"callback_port": 70000}}}}`
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "servers.bad.oauth.callback_port")
}

func TestLoad_ServerHTTPTimeoutSeconds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": {
			"internal": {
				"type": "streamable-http",
				"url": "http://localhost:3000/mcp",
				"http_timeout_seconds": 30
			}
		}
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 30, cfg.Servers["internal"].HTTPTimeoutSeconds)
}

func TestLoad_ServerStartupRetryConfigPreservesAbsentAndZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": {
			"defaulted": {"command": "echo"},
			"disabled": {
				"command": "echo",
				"startup_retry_count": 0,
				"startup_retry_backoff_ms": 0,
				"startup_timeout_seconds": 0
			},
			"custom": {
				"command": "echo",
				"startup_retry_count": 5,
				"startup_retry_backoff_ms": 250,
				"startup_timeout_seconds": 2
			}
		}
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Nil(t, cfg.Servers["defaulted"].StartupRetryCount)
	require.Nil(t, cfg.Servers["defaulted"].StartupRetryBackoffMS)
	require.Nil(t, cfg.Servers["defaulted"].StartupTimeoutSeconds)
	require.Equal(t, 0, *cfg.Servers["disabled"].StartupRetryCount)
	require.Equal(t, 0, *cfg.Servers["disabled"].StartupRetryBackoffMS)
	require.Equal(t, 0, *cfg.Servers["disabled"].StartupTimeoutSeconds)
	require.Equal(t, 5, *cfg.Servers["custom"].StartupRetryCount)
	require.Equal(t, 250, *cfg.Servers["custom"].StartupRetryBackoffMS)
	require.Equal(t, 2, *cfg.Servers["custom"].StartupTimeoutSeconds)
}

func TestLoad_ServerStartupRetryConfigRejectsNegativeValues(t *testing.T) {
	for name, body := range map[string]string{
		"startup retry count":      `{"startup_retry_count": -1}`,
		"startup retry backoff ms": `{"startup_retry_backoff_ms": -1}`,
		"startup timeout seconds":  `{"startup_timeout_seconds": -1}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")

			data := `{"servers":{"bad":` + body + `}}`
			err := os.WriteFile(path, []byte(data), 0o600)
			require.NoError(t, err)

			_, err = Load(path)
			require.Error(t, err)
			require.Contains(t, err.Error(), "servers.bad")
		})
	}
}

func TestLoad_ServerStartupRetryConfigRejectsTooLargeRetryCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := `{"servers":{"bad":{"startup_retry_count": 1001}}}`
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "servers.bad.startup_retry_count")
	require.Contains(t, err.Error(), "1000")
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
// through rules file Load/Save with the "args" key absent in the serialized JSON.
func TestRuleConfig_NoArgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")

	require.NoError(t, SaveRulesFile(path, []RuleConfig{{Tool: "*", Verdict: "require-approval"}}))
	rules, err := LoadRulesFile(path)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Nil(t, rules[0].Args)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	decodedRules, ok := decoded["rules"].([]any)
	require.True(t, ok)
	require.Len(t, decodedRules, 1)

	rule, ok := decodedRules[0].(map[string]any)
	require.True(t, ok)
	_, hasArgs := rule["args"]
	require.False(t, hasArgs, "args key must be absent when rule has no args")
}

// TestRuleConfig_ExactArgRoundTrip verifies that a rule with one exact-string
// arg pattern round-trips through rules file Load/Save and back unchanged in semantics.
func TestRuleConfig_ExactArgRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	original := []RuleConfig{{Tool: "push", Verdict: "allow", Args: []ArgPattern{{Path: "remote", Match: json.RawMessage(`"origin"`)}}}}

	require.NoError(t, SaveRulesFile(path, original))
	rules, err := LoadRulesFile(path)
	require.NoError(t, err)
	require.Len(t, rules, 1)

	rule := rules[0]
	require.Equal(t, "push", rule.Tool)
	require.Equal(t, "allow", rule.Verdict)
	require.Len(t, rule.Args, 1)
	require.Equal(t, "remote", rule.Args[0].Path)
	require.Equal(t, json.RawMessage(`"origin"`), rule.Args[0].Match)

	require.NoError(t, SaveRulesFile(path, rules))
	rules2, err := LoadRulesFile(path)
	require.NoError(t, err)
	require.Equal(t, rules[0].Tool, rules2[0].Tool)
	require.Equal(t, rules[0].Verdict, rules2[0].Verdict)
	require.Len(t, rules2[0].Args, 1)
	require.Equal(t, "remote", rules2[0].Args[0].Path)
	require.Equal(t, json.RawMessage(`"origin"`), rules2[0].Args[0].Match)
}

// TestRuleConfig_MixedArgsRoundTrip verifies a rule with mixed exact + regex
// arg patterns round-trips correctly, keeping Match as raw JSON.
func TestRuleConfig_MixedArgsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	original := []RuleConfig{{
		Tool:    "push",
		Verdict: "allow",
		Args: []ArgPattern{
			{Path: "remote", Match: json.RawMessage(`"origin"`)},
			{Path: "commit.message", Match: json.RawMessage(`{"regex":"^feat:"}`)},
		},
	}}

	require.NoError(t, SaveRulesFile(path, original))
	rules, err := LoadRulesFile(path)
	require.NoError(t, err)
	require.Len(t, rules, 1)

	rule := rules[0]
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

	require.NoError(t, SaveRulesFile(path, rules))
	rules2, err := LoadRulesFile(path)
	require.NoError(t, err)
	require.Len(t, rules2[0].Args, 2)

	// Regex RawMessage decodes to the same object after round-trip.
	var regexObj2 map[string]string
	require.NoError(t, json.Unmarshal(rules2[0].Args[1].Match, &regexObj2))
	require.Equal(t, "^feat:", regexObj2["regex"])
}
