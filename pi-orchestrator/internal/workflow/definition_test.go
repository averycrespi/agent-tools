package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFileAcceptsValidV1Definition(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pr-review.yaml")
	writeFile(t, path, `name: pr-review
description: Review a pull request
repo: "{{ .Inputs.repo }}"
inputs:
  repo:
    type: string
    required: true
  pr_number:
    type: integer
    required: true
  draft:
    type: boolean
    default: false
agents:
  reviewer:
    model: gpt-5.1-codex
    skills: [review]
steps:
  - id: review
    agent: reviewer
    prompt: "Write findings to {{ artifact_path \"findings\" }}"
    artifacts:
      - name: findings
        path: findings.md
        required: true
`)

	def, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}

	if def.Name != "pr-review" {
		t.Fatalf("Name = %q, want pr-review", def.Name)
	}
	if got := len(def.Steps); got != 1 {
		t.Fatalf("len(Steps) = %d, want 1", got)
	}
}

func TestLoadFileRejectsFilenameNameMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "expected.yaml")
	writeFile(t, path, minimalWorkflow("actual"))

	_, err := LoadFile(path)
	assertErrorContains(t, err, "workflow name actual must match filename stem expected")
}

func TestLoadFileRejectsUnsupportedV1Field(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, minimalWorkflow("sample")+"schedule: daily\n")

	_, err := LoadFile(path)
	assertErrorContains(t, err, "unsupported workflow field schedule")
}

func TestValidateRejectsInvalidInputSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
inputs:
  count:
    type: float
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: run
    agent: runner
    prompt: run
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "input count has unsupported type float")
}

func TestValidateRejectsUnknownStepAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: run
    agent: missing
    prompt: run
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "step run references unknown agent missing")
}

func TestValidateRejectsCycles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: first
    agent: runner
    needs: [second]
    prompt: first
  - id: second
    agent: runner
    needs: [first]
    prompt: second
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "step dependencies contain a cycle")
}

func TestValidateRejectsAbsoluteArtifactPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: run
    agent: runner
    prompt: run
    artifacts:
      - name: out
        path: /tmp/out.txt
        required: true
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "artifact out path must be relative")
}

func minimalWorkflow(name string) string {
	return `name: ` + name + `
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: run
    agent: runner
    prompt: run
`
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want substring %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err.Error(), want)
	}
}
