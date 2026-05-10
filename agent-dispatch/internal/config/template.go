package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
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
	Command                        string   `json:"command"`
	Mode                           string   `json:"mode"`
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
			name := tmpl.Name
			if name == "" {
				name = strings.TrimSuffix(entry.Name(), ".json")
				tmpl.Name = name
			}
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
		if os.IsNotExist(err) || errorsIsNotExist(err) {
			return Template{}, fmt.Errorf("template not found %q: %w", path, err)
		}
		return Template{}, fmt.Errorf("read template %q: %w", path, err)
	}
	var tmpl Template
	if err := json.Unmarshal(data, &tmpl); err != nil {
		return Template{}, fmt.Errorf("parse template %q: %w", path, err)
	}
	tmpl.Path = path
	if tmpl.Agent.Command == "" {
		tmpl.Agent.Command = "pi"
	}
	if tmpl.Agent.Mode == "" {
		tmpl.Agent.Mode = "rpc"
	}
	return tmpl, nil
}

func FindTemplate(dirs []string, name string) (Template, error) {
	if name == "" {
		return Template{Agent: AgentTemplate{Command: "pi", Mode: "rpc"}}, nil
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

func RenderPiArgv(agent AgentTemplate) []string {
	command := agent.Command
	if command == "" {
		command = "pi"
	}
	mode := agent.Mode
	if mode == "" {
		mode = "rpc"
	}
	argv := []string{command, "--mode", mode}
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
	return argv
}

func errorsIsNotExist(err error) bool {
	return err == fs.ErrNotExist
}
