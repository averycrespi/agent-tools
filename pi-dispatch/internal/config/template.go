package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Template struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Agent       AgentTemplate `json:"agent"`
	Path        string        `json:"-"`
}

type AgentTemplate struct {
	Provider                       string   `json:"provider"`
	Model                          string   `json:"model"`
	Thinking                       string   `json:"thinking"`
	Tools                          []string `json:"tools"`
	DisableBuiltinTools            bool     `json:"disable_builtin_tools"`
	DisableAllTools                bool     `json:"disable_all_tools"`
	Extensions                     []string `json:"extensions"`
	DisableExtensionDiscovery      bool     `json:"disable_extension_discovery"`
	Skills                         []string `json:"skills"`
	DisableSkillDiscovery          bool     `json:"disable_skill_discovery"`
	PromptTemplates                []string `json:"prompt_templates"`
	DisablePromptTemplateDiscovery bool     `json:"disable_prompt_template_discovery"`
	DisableContextFiles            bool     `json:"disable_context_files"`
	SystemPrompt                   string   `json:"system_prompt"`
	AppendSystemPrompt             string   `json:"append_system_prompt"`
	SessionDir                     string   `json:"session_dir"`
}

type templateFile struct {
	Description string        `json:"description"`
	Agent       AgentTemplate `json:"agent"`
}

func DiscoverTemplates(dirs []string) ([]Template, error) {
	var templates []Template
	seen := map[string]string{}
	for _, dir := range dirs {
		dir = ExpandTilde(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read template dir %q: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			tmpl, err := LoadTemplate(path)
			if err != nil {
				return nil, err
			}
			if err := ValidateTemplate(tmpl); err != nil {
				return nil, err
			}
			name := tmpl.Name
			if prev, ok := seen[name]; ok {
				return nil, fmt.Errorf("duplicate template %q in %s and %s", name, prev, path)
			}
			seen[name] = path
			templates = append(templates, tmpl)
		}
	}
	return templates, nil
}

func LoadTemplate(path string) (Template, error) {
	data, err := os.ReadFile(path) //nolint:gosec // user-selected template path.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Template{}, fmt.Errorf("template not found %q: %w", path, err)
		}
		return Template{}, fmt.Errorf("read template %q: %w", path, err)
	}
	var file templateFile
	if err := decodeStrict(data, &file); err != nil {
		return Template{}, fmt.Errorf("parse template %q: %w", path, err)
	}
	tmpl := Template{
		Name:        strings.TrimSuffix(filepath.Base(path), ".json"),
		Description: file.Description,
		Agent:       file.Agent,
		Path:        path,
	}
	return tmpl, nil
}

func FindTemplate(dirs []string, name string) (Template, error) {
	if name == "" {
		return Template{}, nil
	}
	templates, err := DiscoverTemplates(dirs)
	if err != nil {
		return Template{}, err
	}
	for _, tmpl := range templates {
		if tmpl.Name == name {
			return tmpl, nil
		}
	}
	return Template{}, fmt.Errorf("template %q not found", name)
}

func ValidateTemplate(tmpl Template) error {
	if tmpl.Agent.DisableAllTools && countNonEmpty(tmpl.Agent.Tools) > 0 {
		return fmt.Errorf("template %q: disable_all_tools cannot be combined with tools", tmpl.Name)
	}
	return nil
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func countNonEmpty(values []string) int {
	count := 0
	for _, value := range values {
		if value != "" {
			count++
		}
	}
	return count
}

func RenderPiArgv(agent AgentTemplate) []string {
	argv := []string{"pi", "--mode", "rpc"}
	addValue := func(flag, value string) {
		if value != "" {
			argv = append(argv, flag, value)
		}
	}
	addList := func(flag string, values []string) {
		for _, value := range values {
			if value != "" {
				argv = append(argv, flag, value)
			}
		}
	}
	addBool := func(flag string, value bool) {
		if value {
			argv = append(argv, flag)
		}
	}
	addValue("--provider", agent.Provider)
	addValue("--model", agent.Model)
	addValue("--thinking", agent.Thinking)
	addList("--tools", agent.Tools)
	addBool("--no-builtin-tools", agent.DisableBuiltinTools)
	addBool("--no-tools", agent.DisableAllTools)
	addList("--extension", agent.Extensions)
	addBool("--no-extensions", agent.DisableExtensionDiscovery)
	addList("--skill", agent.Skills)
	addBool("--no-skills", agent.DisableSkillDiscovery)
	addList("--prompt-template", agent.PromptTemplates)
	addBool("--no-prompt-templates", agent.DisablePromptTemplateDiscovery)
	addBool("--no-context-files", agent.DisableContextFiles)
	addValue("--system-prompt", agent.SystemPrompt)
	addValue("--append-system-prompt", agent.AppendSystemPrompt)
	addValue("--session-dir", agent.SessionDir)
	return argv
}

func ApplyAgentOverrides(base, overrides AgentTemplate) AgentTemplate {
	if overrides.Provider != "" {
		base.Provider = overrides.Provider
	}
	if overrides.Model != "" {
		base.Model = overrides.Model
	}
	if overrides.Thinking != "" {
		base.Thinking = overrides.Thinking
	}
	if overrides.Tools != nil {
		base.Tools = overrides.Tools
	}
	if overrides.DisableBuiltinTools {
		base.DisableBuiltinTools = true
	}
	if overrides.DisableAllTools {
		base.DisableAllTools = true
	}
	if overrides.Extensions != nil {
		base.Extensions = overrides.Extensions
	}
	if overrides.DisableExtensionDiscovery {
		base.DisableExtensionDiscovery = true
	}
	if overrides.Skills != nil {
		base.Skills = overrides.Skills
	}
	if overrides.DisableSkillDiscovery {
		base.DisableSkillDiscovery = true
	}
	if overrides.PromptTemplates != nil {
		base.PromptTemplates = overrides.PromptTemplates
	}
	if overrides.DisablePromptTemplateDiscovery {
		base.DisablePromptTemplateDiscovery = true
	}
	if overrides.DisableContextFiles {
		base.DisableContextFiles = true
	}
	if overrides.SystemPrompt != "" {
		base.SystemPrompt = overrides.SystemPrompt
	}
	if overrides.AppendSystemPrompt != "" {
		base.AppendSystemPrompt = overrides.AppendSystemPrompt
	}
	if overrides.SessionDir != "" {
		base.SessionDir = overrides.SessionDir
	}
	return base
}
