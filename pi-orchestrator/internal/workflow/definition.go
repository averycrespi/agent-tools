package workflow

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

const (
	InputString  = "string"
	InputInteger = "integer"
	InputBoolean = "boolean"

	ProduceExists   ArtifactProduceCheck = "exists"
	ProduceNonEmpty ArtifactProduceCheck = "non_empty"
)

var templateSafeIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Definition struct {
	Name        string                  `yaml:"name"`
	Description string                  `yaml:"description"`
	Repo        string                  `yaml:"repo"`
	Inputs      map[string]InputSchema  `yaml:"inputs"`
	Agents      map[string]Agent        `yaml:"agents"`
	Artifacts   map[string]RootArtifact `yaml:"artifacts"`
	Steps       []Step                  `yaml:"steps"`
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
	ID       string                          `yaml:"id"`
	Agent    string                          `yaml:"agent"`
	Needs    []string                        `yaml:"needs"`
	Prompt   string                          `yaml:"prompt"`
	Produces map[string]ArtifactProduceCheck `yaml:"produces"`
}

type RootArtifact struct {
	Path string `yaml:"path"`
}

type ArtifactProduceCheck string

func LoadFile(path string) (*Definition, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- workflow paths are selected from the configured workflow definition directory.
	if err != nil {
		return nil, fmt.Errorf("read workflow %s: %w", path, err)
	}

	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return LoadBytes(data, stem, path)
}

func LoadBytes(data []byte, filenameStem string, source string) (*Definition, error) {
	if err := rejectUnsupportedTopLevelFields(data); err != nil {
		return nil, err
	}

	var def Definition
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&def); err != nil {
		return nil, fmt.Errorf("parse workflow %s: %w", source, err)
	}

	if err := def.Validate(filenameStem); err != nil {
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
		if input.Default != nil {
			value, err := coerceDefault(name, input.Type, input.Default)
			if err != nil {
				return err
			}
			if len(input.Enum) > 0 && !enumContains(input.Enum, value) {
				return fmt.Errorf("input %s must be one of %s", name, strings.Join(input.Enum, ", "))
			}
		}
	}

	for name, artifact := range d.Artifacts {
		if !templateSafeIdentifier.MatchString(name) {
			return fmt.Errorf("artifact %s name must match [A-Za-z_][A-Za-z0-9_]*", name)
		}
		if artifact.Path == "" {
			return fmt.Errorf("artifact %s path is required", name)
		}
		if filepath.IsAbs(artifact.Path) {
			return fmt.Errorf("artifact %s path must be relative", name)
		}
		if containsDotDot(artifact.Path) {
			return fmt.Errorf("artifact %s path must not contain parent directory references", name)
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
		if strings.TrimSpace(step.Prompt) == "" {
			return fmt.Errorf("step %s prompt is required", step.ID)
		}
		for name, check := range step.Produces {
			if _, exists := d.Artifacts[name]; !exists {
				return fmt.Errorf("step %s produces unknown artifact %s", step.ID, name)
			}
			if check != ProduceExists && check != ProduceNonEmpty {
				return fmt.Errorf("step %s produces artifact %s has unsupported check %s", step.ID, name, check)
			}
		}
		if err := ValidatePromptTemplate(step.ID, step.Prompt, d.Inputs, d.Artifacts); err != nil {
			return err
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

func ValidatePromptTemplate(stepID string, prompt string, inputs map[string]InputSchema, artifacts map[string]RootArtifact) error {
	inputData := make(map[string]any, len(inputs))
	for name := range inputs {
		inputData[name] = ""
	}
	artifactData := make(map[string]string, len(artifacts))
	for name := range artifacts {
		artifactData[name] = ""
	}
	tmpl, err := template.New("step-prompt").Option("missingkey=error").Parse(prompt)
	if err != nil {
		return fmt.Errorf("parse prompt for step %s: %w", stepID, err)
	}
	if err := tmpl.Execute(ioDiscard{}, map[string]any{"Inputs": inputData, "Artifacts": artifactData}); err != nil {
		return fmt.Errorf("validate prompt for step %s: %w", stepID, err)
	}
	return nil
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
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
		"artifacts":   {},
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
