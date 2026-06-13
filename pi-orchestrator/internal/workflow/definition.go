package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	InputString  = "string"
	InputInteger = "integer"
	InputBoolean = "boolean"
)

type Definition struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Repo        string                 `yaml:"repo"`
	Inputs      map[string]InputSchema `yaml:"inputs"`
	Agents      map[string]Agent       `yaml:"agents"`
	Steps       []Step                 `yaml:"steps"`
}

type InputSchema struct {
	Type     string   `yaml:"type"`
	Required bool     `yaml:"required"`
	Default  any      `yaml:"default"`
	Enum     []string `yaml:"enum"`
}

type Agent struct {
	Model  string   `yaml:"model"`
	Skills []string `yaml:"skills"`
}

type Step struct {
	ID        string     `yaml:"id"`
	Agent     string     `yaml:"agent"`
	Needs     []string   `yaml:"needs"`
	Prompt    string     `yaml:"prompt"`
	Artifacts []Artifact `yaml:"artifacts"`
}

type Artifact struct {
	Name     string `yaml:"name"`
	Path     string `yaml:"path"`
	Required bool   `yaml:"required"`
}

func LoadFile(path string) (*Definition, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- workflow paths are selected from the configured workflow definition directory.
	if err != nil {
		return nil, fmt.Errorf("read workflow %s: %w", path, err)
	}

	if err := rejectUnsupportedTopLevelFields(data); err != nil {
		return nil, err
	}

	var def Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("parse workflow %s: %w", path, err)
	}

	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if err := def.Validate(stem); err != nil {
		return nil, err
	}
	return &def, nil
}

func (d *Definition) Validate(filenameStem string) error {
	if d.Name == "" {
		return fmt.Errorf("workflow name is required")
	}
	if d.Name != filenameStem {
		return fmt.Errorf("workflow name %s must match filename stem %s", d.Name, filenameStem)
	}
	if d.Repo == "" {
		return fmt.Errorf("workflow repo is required")
	}
	if len(d.Agents) == 0 {
		return fmt.Errorf("workflow must define at least one agent")
	}
	if len(d.Steps) == 0 {
		return fmt.Errorf("workflow must define at least one step")
	}

	for name, input := range d.Inputs {
		if input.Type != InputString && input.Type != InputInteger && input.Type != InputBoolean {
			return fmt.Errorf("input %s has unsupported type %s", name, input.Type)
		}
	}

	stepIDs := make(map[string]struct{}, len(d.Steps))
	for _, step := range d.Steps {
		if step.ID == "" {
			return fmt.Errorf("step id is required")
		}
		if _, exists := stepIDs[step.ID]; exists {
			return fmt.Errorf("step %s is duplicated", step.ID)
		}
		stepIDs[step.ID] = struct{}{}
		if _, exists := d.Agents[step.Agent]; !exists {
			return fmt.Errorf("step %s references unknown agent %s", step.ID, step.Agent)
		}
		for _, artifact := range step.Artifacts {
			if artifact.Name == "" {
				return fmt.Errorf("step %s artifact name is required", step.ID)
			}
			if artifact.Path == "" {
				return fmt.Errorf("artifact %s path is required", artifact.Name)
			}
			if filepath.IsAbs(artifact.Path) {
				return fmt.Errorf("artifact %s path must be relative", artifact.Name)
			}
			if containsDotDot(artifact.Path) {
				return fmt.Errorf("artifact %s path must not contain parent directory references", artifact.Name)
			}
		}
	}

	for _, step := range d.Steps {
		for _, need := range step.Needs {
			if _, exists := stepIDs[need]; !exists {
				return fmt.Errorf("step %s needs unknown step %s", step.ID, need)
			}
		}
	}
	if hasCycle(d.Steps) {
		return fmt.Errorf("step dependencies contain a cycle")
	}
	return nil
}

func rejectUnsupportedTopLevelFields(data []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return fmt.Errorf("parse workflow: %w", err)
	}
	allowed := map[string]struct{}{
		"name":        {},
		"description": {},
		"repo":        {},
		"inputs":      {},
		"agents":      {},
		"steps":       {},
	}
	if len(node.Content) == 0 || node.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("workflow must be a YAML mapping")
	}
	mapping := node.Content[0]
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i].Value
		if _, exists := allowed[key]; !exists {
			return fmt.Errorf("unsupported workflow field %s", key)
		}
	}
	return nil
}

func containsDotDot(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func hasCycle(steps []Step) bool {
	needs := make(map[string][]string, len(steps))
	for _, step := range steps {
		needs[step.ID] = step.Needs
	}

	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, need := range needs[id] {
			if visit(need) {
				return true
			}
		}
		visiting[id] = false
		visited[id] = true
		return false
	}
	for _, step := range steps {
		if visit(step.ID) {
			return true
		}
	}
	return false
}
