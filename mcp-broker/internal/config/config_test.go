package config

import (
	"encoding/json"
	"fmt"
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
	require.Equal(t, filepath.Join(dir, "rules.json"), cfg.Rules.Path)
	require.FileExists(t, path)

	rulesResult, err := LoadRulesForConfig(path, cfg)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "rules.json"), rulesResult.Path)
	require.Equal(t, DefaultRules(), rulesResult.Rules)
	require.FileExists(t, rulesResult.Path)

	rawConfig, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(rawConfig), `"rules": {`)
	require.Contains(t, string(rawConfig), `"path":`)
	require.NotContains(t, string(rawConfig), `"rules_path"`)

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
	require.Equal(t, filepath.Join(dir, "profile", "rules.json"), cfg.Rules.Path)

	result, err := LoadRulesForConfig(path, cfg)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "profile", "rules.json"), result.Path)
}

func TestResolveRulesPathReadsNestedRulesPath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	customRulesPath := filepath.Join(dir, "custom-rules.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"rules": {"path": "`+customRulesPath+`"}
	}`), 0o600))

	resolved, err := ResolveRulesPath(configPath)
	require.NoError(t, err)
	require.Equal(t, customRulesPath, resolved)
}

func TestLoadAcceptsLegacyRulesPath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	legacyRulesPath := filepath.Join(dir, "legacy-rules.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"rules_path": "`+legacyRulesPath+`"
	}`), 0o600))

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.Equal(t, legacyRulesPath, cfg.Rules.Path)

	resolved, err := ResolveRulesPath(configPath)
	require.NoError(t, err)
	require.Equal(t, legacyRulesPath, resolved)
}

func TestLoadPrefersNestedRulesPathOverLegacyAlias(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	nestedRulesPath := filepath.Join(dir, "nested-rules.json")
	legacyRulesPath := filepath.Join(dir, "legacy-rules.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"rules": {"path": "`+nestedRulesPath+`"},
		"rules_path": "`+legacyRulesPath+`"
	}`), 0o600))

	cfg, err := Load(configPath)
	require.NoError(t, err)
	require.Equal(t, nestedRulesPath, cfg.Rules.Path)

	resolved, err := ResolveRulesPath(configPath)
	require.NoError(t, err)
	require.Equal(t, nestedRulesPath, resolved)
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
		"rules": [{"tool": "*", "verdict": "allow"}]
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
	require.Contains(t, string(rawConfig), `"rules": {`)
	require.Contains(t, string(rawConfig), `"path":`)
	require.NotContains(t, string(rawConfig), `"rules_path"`)
}

func TestRefresh_RewritesLegacyRulesPathAsNestedRulesPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	rulesPath := filepath.Join(dir, "custom-rules.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"rules_path":"`+rulesPath+`"}`), 0o600))

	_, err := Refresh(path)
	require.NoError(t, err)

	rawConfig, err := os.ReadFile(path)
	require.NoError(t, err)
	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rawConfig, &root))
	require.NotContains(t, root, "rules_path")
	var rules RulesConfig
	require.NoError(t, json.Unmarshal(root["rules"], &rules))
	require.Equal(t, rulesPath, rules.Path)
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

func TestLoad_HooksDefaultsAndEmptyForms(t *testing.T) {
	for name, body := range map[string]string{
		"omitted":      `{}`,
		"empty object": `{"hooks":{}}`,
		"empty event":  `{"hooks":{"events":{"require-approval":[]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

			cfg, err := Load(path)
			require.NoError(t, err)
			require.Equal(t, HookDispatchConfig{
				MaxConcurrent:   4,
				QueueSize:       64,
				MaxPayloadBytes: 10 * 1024 * 1024,
				MaxQueuedBytes:  64 * 1024 * 1024,
			}, cfg.Hooks.Dispatch)
			require.Empty(t, cfg.Hooks.Events.RequireApproval)
			require.NotNil(t, cfg.Hooks.Events.RequireApproval)
		})
	}
}

func TestRefresh_HooksWritesCanonicalDefaultsAndPreservesRawEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"hooks":{"events":{"require-approval":[{
			"command":"notify",
			"timeout_seconds":10,
			"env":{"TOKEN":"$TOKEN","CHANNEL":"${CHANNEL}"}
		}]}}
	}`), 0o600))

	_, err := Refresh(path)
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, json.Unmarshal(raw, &root))
	hooks := root["hooks"].(map[string]any)
	dispatch := hooks["dispatch"].(map[string]any)
	require.EqualValues(t, 4, dispatch["max_concurrent"])
	require.EqualValues(t, 64, dispatch["queue_size"])
	require.EqualValues(t, 10*1024*1024, dispatch["max_payload_bytes"])
	require.EqualValues(t, 64*1024*1024, dispatch["max_queued_bytes"])
	handlers := hooks["events"].(map[string]any)["require-approval"].([]any)
	env := handlers[0].(map[string]any)["env"].(map[string]any)
	require.Equal(t, "$TOKEN", env["TOKEN"])
	require.Equal(t, "${CHANNEL}", env["CHANNEL"])
}

func TestLoad_HooksAcceptsMultipleHandlers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"hooks":{"events":{"require-approval":[
			{"command":"/bin/echo","args":["one"],"timeout_seconds":1},
			{"command":"notify","args":[],"timeout_seconds":86400,"env":{"SAFE_KEY":"value"}}
		]}}
	}`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Hooks.Events.RequireApproval, 2)
	require.Equal(t, []string{"one"}, cfg.Hooks.Events.RequireApproval[0].Args)
	require.Equal(t, "value", cfg.Hooks.Events.RequireApproval[1].Env["SAFE_KEY"])
}

func TestLoad_HooksRejectsInvalidConfiguration(t *testing.T) {
	const mib = 1024 * 1024
	tests := map[string]struct {
		hooks string
		want  string
	}{
		"max concurrent low":   {`{"dispatch":{"max_concurrent":0}}`, "hooks.dispatch.max_concurrent"},
		"max concurrent high":  {`{"dispatch":{"max_concurrent":65}}`, "hooks.dispatch.max_concurrent"},
		"queue size low":       {`{"dispatch":{"queue_size":0}}`, "hooks.dispatch.queue_size"},
		"queue size high":      {`{"dispatch":{"queue_size":4097}}`, "hooks.dispatch.queue_size"},
		"payload low":          {`{"dispatch":{"max_payload_bytes":0}}`, "hooks.dispatch.max_payload_bytes"},
		"payload high":         {fmt.Sprintf(`{"dispatch":{"max_payload_bytes":%d,"max_queued_bytes":%d}}`, 64*mib+1, 64*mib+1), "hooks.dispatch.max_payload_bytes"},
		"queued below payload": {`{"dispatch":{"max_payload_bytes":1024,"max_queued_bytes":1023}}`, "hooks.dispatch.max_queued_bytes"},
		"queued high":          {fmt.Sprintf(`{"dispatch":{"max_queued_bytes":%d}}`, 512*mib+1), "hooks.dispatch.max_queued_bytes"},
		"unknown event":        {`{"events":{"after-call":[]}}`, "unknown hook event"},
		"empty command":        {`{"events":{"require-approval":[{"command":"","timeout_seconds":1}]}}`, "command must not be empty"},
		"missing timeout":      {`{"events":{"require-approval":[{"command":"notify"}]}}`, "timeout_seconds"},
		"timeout high":         {`{"events":{"require-approval":[{"command":"notify","timeout_seconds":86401}]}}`, "timeout_seconds"},
		"invalid env key":      {`{"events":{"require-approval":[{"command":"notify","timeout_seconds":1,"env":{"BAD-KEY":"x"}}]}}`, "environment key"},
		"nul command":          {`{"events":{"require-approval":[{"command":"bad\u0000cmd","timeout_seconds":1}]}}`, "NUL"},
		"nul arg":              {`{"events":{"require-approval":[{"command":"notify","args":["bad\u0000arg"],"timeout_seconds":1}]}}`, "NUL"},
		"nul env key":          {`{"events":{"require-approval":[{"command":"notify","timeout_seconds":1,"env":{"BAD\u0000KEY":"value"}}]}}`, "NUL"},
		"nul env value":        {`{"events":{"require-approval":[{"command":"notify","timeout_seconds":1,"env":{"KEY":"bad\u0000value"}}]}}`, "NUL"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			require.NoError(t, os.WriteFile(path, []byte(`{"hooks":`+tc.hooks+`}`), 0o600))
			_, err := Load(path)
			require.ErrorContains(t, err, tc.want)
		})
	}
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
