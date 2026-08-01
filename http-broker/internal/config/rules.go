package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/http-broker/internal/rules"
)

// EffectiveRulesPath returns the configured rules file path, defaulting to
// rules.json alongside the effective config file.
func EffectiveRulesPath(configPath string, cfg Config) string {
	if cfg.RulesPath != "" {
		return cfg.RulesPath
	}
	return filepath.Join(filepath.Dir(configPath), "rules.json")
}

// DefaultRulesDocument returns the rules document generated for a fresh
// install.
//
// It ships fallthrough "tunnel", not "deny". A fresh sandbox with no rules and
// fallthrough "deny" has no working network at all, which reads as a broken
// install rather than a policy decision. The commented starter rules show the
// shape of a tightened policy; the README documents tightening as the intended
// end state.
func DefaultRulesDocument() rules.Document {
	return rules.Document{
		Fallthrough: rules.FallthroughTunnel,
		Rules:       []rules.Rule{},
	}
}

// LoadRulesFile reads and validates a rules document.
//
// A file-level parse error names the file and the byte offset; a per-rule
// validation error names the rule (AC-6).
func LoadRulesFile(path string) (rules.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rules.Document{}, err
	}

	var doc rules.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return rules.Document{}, fmt.Errorf("parsing %s: %w", path, describeJSONError(data, err))
	}
	return doc, nil
}

// LoadRulesEngine reads, validates and compiles the rules document at path.
func LoadRulesEngine(path string) (*rules.Engine, error) {
	doc, err := LoadRulesFile(path)
	if err != nil {
		return nil, err
	}
	engine, err := rules.New(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return engine, nil
}

// SaveRulesFile writes a rules document in canonical form.
func SaveRulesFile(path string, doc rules.Document) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if doc.Rules == nil {
		doc.Rules = []rules.Rule{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// LoadRulesForConfig loads the rules document for cfg, generating the default
// document when the file does not yet exist.
func LoadRulesForConfig(configPath string, cfg Config) (string, rules.Document, error) {
	path := EffectiveRulesPath(configPath, cfg)

	doc, err := LoadRulesFile(path)
	if err == nil {
		return path, doc, nil
	}
	if !os.IsNotExist(err) {
		return path, rules.Document{}, err
	}

	doc = DefaultRulesDocument()
	if err := SaveRulesFile(path, doc); err != nil {
		return path, rules.Document{}, err
	}
	return path, doc, nil
}

// RefreshRules loads or creates the effective rules file, validates it, and
// rewrites it in canonical form. Returns the path written.
func RefreshRules(configPath string) (string, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return "", err
	}
	path, doc, err := LoadRulesForConfig(configPath, cfg)
	if err != nil {
		return "", err
	}
	if _, err := rules.New(doc); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if err := SaveRulesFile(path, doc); err != nil {
		return "", err
	}
	return path, nil
}
