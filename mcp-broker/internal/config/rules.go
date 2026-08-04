package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RulesLoadResult describes the effective base policy rules source.
type RulesLoadResult struct {
	Path           string
	Rules          []RuleConfig
	MigratedLegacy bool
	IgnoredLegacy  bool
}

type rulesDocument struct {
	Rules []RuleConfig `json:"rules"`
}

// EffectiveRulesPath returns the configured rules file path, defaulting to
// rules.json alongside the effective config file.
func EffectiveRulesPath(configPath string, cfg Config) string {
	if cfg.Rules.Path != "" {
		return cfg.Rules.Path
	}
	return filepath.Join(filepath.Dir(configPath), "rules.json")
}

// ResolveRulesPath reads only rules.path (or the legacy rules_path alias) from
// configPath, defaulting to rules.json alongside the effective config file.
func ResolveRulesPath(configPath string) (string, error) {
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return EffectiveRulesPath(configPath, Config{}), nil
	}
	if err != nil {
		return "", err
	}

	var wire struct {
		Rules           json.RawMessage `json:"rules"`
		LegacyRulesPath *string         `json:"rules_path"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return "", fmt.Errorf("parsing config JSON: %w", err)
	}

	var cfg Config
	trimmedRules := bytes.TrimSpace(wire.Rules)
	if len(trimmedRules) > 0 && trimmedRules[0] == '{' {
		if err := json.Unmarshal(trimmedRules, &cfg.Rules); err != nil {
			return "", fmt.Errorf("parsing config rules: %w", err)
		}
	} else if wire.LegacyRulesPath != nil {
		cfg.Rules.Path = *wire.LegacyRulesPath
	}
	return EffectiveRulesPath(configPath, cfg), nil
}

// LoadRulesForConfig loads or creates the base policy rules for cfg.
// If legacy embedded config rules exist and the external rules file is missing,
// they are migrated to the rules file. If both exist, the rules file wins.
func LoadRulesForConfig(configPath string, cfg Config) (RulesLoadResult, error) {
	path := EffectiveRulesPath(configPath, cfg)
	legacyRaw, hasLegacy, err := legacyRulesRaw(configPath)
	if err != nil {
		return RulesLoadResult{Path: path}, err
	}

	rules, err := LoadRulesFile(path)
	if err == nil {
		return RulesLoadResult{Path: path, Rules: rules, IgnoredLegacy: hasLegacy}, nil
	}
	if !os.IsNotExist(err) {
		return RulesLoadResult{Path: path}, err
	}

	if hasLegacy {
		rules, err := parseRulesRaw(legacyRaw)
		if err != nil {
			return RulesLoadResult{Path: path}, fmt.Errorf("parsing legacy config rules: %w", err)
		}
		if err := SaveRulesFile(path, rules); err != nil {
			return RulesLoadResult{Path: path}, err
		}
		return RulesLoadResult{Path: path, Rules: rules, MigratedLegacy: true}, nil
	}

	rules = DefaultRules()
	if err := SaveRulesFile(path, rules); err != nil {
		return RulesLoadResult{Path: path}, err
	}
	return RulesLoadResult{Path: path, Rules: rules}, nil
}

// LoadRulesFile reads policy rules from a canonical rules document.
func LoadRulesFile(path string) ([]RuleConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing rules JSON: %w", err)
	}

	raw, ok := root["rules"]
	if !ok {
		return nil, fmt.Errorf("rules must contain a rules array")
	}
	return parseRulesRaw(raw)
}

// SaveRulesFile writes policy rules in canonical document form.
func SaveRulesFile(path string, rules []RuleConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	data, err := json.MarshalIndent(rulesDocument{Rules: rules}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// RefreshRules loads or creates the effective rules file and rewrites it in
// canonical form. It returns the path written.
func RefreshRules(configPath string) (string, error) {
	result, err := RefreshRulesWithResult(configPath)
	if err != nil {
		return "", err
	}
	return result.Path, nil
}

// RefreshRulesWithResult refreshes the rules file and returns source metadata.
func RefreshRulesWithResult(configPath string) (RulesLoadResult, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return RulesLoadResult{}, err
	}
	result, err := LoadRulesForConfig(configPath, cfg)
	if err != nil {
		return result, err
	}
	if err := SaveRulesFile(result.Path, result.Rules); err != nil {
		return result, err
	}
	return result, nil
}

func parseRulesRaw(raw json.RawMessage) ([]RuleConfig, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("rules must be an array")
	}
	var rules []RuleConfig
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("parsing rules: %w", err)
	}
	return rules, nil
}

func legacyRulesRaw(configPath string) (json.RawMessage, bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, false, err
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("parsing config JSON: %w", err)
	}
	raw, ok := root["rules"]
	if !ok {
		return nil, false, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return nil, false, nil
	}
	return raw, true, nil
}
