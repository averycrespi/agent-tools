package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// LoadRules reads only the policy rules from an existing config file.
// If the rules field is omitted, startup's default rules are returned.
func LoadRules(path string) ([]RuleConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing config JSON: %w", err)
	}

	raw, ok := root["rules"]
	if !ok {
		return DefaultConfig().Rules, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("rules must be an array")
	}

	var rules []RuleConfig
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("parsing rules: %w", err)
	}
	return rules, nil
}
