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
artifacts:
  findings:
    path: findings.md
steps:
  - id: review
    agent: reviewer
    prompt: "Write findings to {{ .Artifacts.findings }} for {{ .Inputs.pr_number }}"
    produces:
      findings: non_empty
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
	if def.Artifacts["findings"].Path != "findings.md" {
		t.Fatalf("Artifacts[findings] = %+v", def.Artifacts["findings"])
	}
	if def.Steps[0].Produces["findings"] != ProduceNonEmpty {
		t.Fatalf("Produces[findings] = %q, want non_empty", def.Steps[0].Produces["findings"])
	}
}

func TestLoadFileValidatesPromptTemplatesWithTypedInputData(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
inputs:
  count:
    type: integer
  draft:
    type: boolean
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: run
    agent: runner
    prompt: '{{ if eq .Inputs.count 0 }}zero{{ end }} {{ if not .Inputs.draft }}not draft{{ end }}'
`)

	if _, err := LoadFile(path); err != nil {
		t.Fatalf("LoadFile() error = %v", err)
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

func TestLoadFileRejectsUnsupportedNestedV1Field(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
inputs:
  repo:
    type: string
    secret: true
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: run
    agent: runner
    prompt: run
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "field secret not found")
}

func TestValidateRejectsEmptyStepPrompt(t *testing.T) {
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
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "step run prompt is required")
}

func TestValidateRejectsInputDefaultThatDoesNotMatchType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
inputs:
  count:
    type: integer
    default: many
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: run
    agent: runner
    prompt: run
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "input count must be an integer")
}

func TestValidateRejectsInputEnumDefaultOutsideAllowedValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
inputs:
  priority:
    type: string
    default: medium
    enum: [high, low]
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: run
    agent: runner
    prompt: run
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "input priority must be one of high, low")
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

func TestValidateRejectsArtifactPathTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
artifacts:
  out:
    path: ../out.txt
steps:
  - id: run
    agent: runner
    prompt: run
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "artifact out path must not contain parent directory references")
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
artifacts:
  out:
    path: /tmp/out.txt
steps:
  - id: run
    agent: runner
    prompt: run
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "artifact out path must be relative")
}

func TestValidateRejectsInvalidArtifactName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
artifacts:
  out-file:
    path: out.txt
steps:
  - id: run
    agent: runner
    prompt: run
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "artifact out-file name must match")
}

func TestLoadFileRejectsStepScopedArtifacts(t *testing.T) {
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
        path: out.txt
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "field artifacts not found")
}

func TestLoadFileRejectsUnsupportedRootArtifactFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
artifacts:
  out:
    name: out
    path: out.txt
    required: true
steps:
  - id: run
    agent: runner
    prompt: run
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "field name not found")
}

func TestValidateRejectsUnknownProducedArtifact(t *testing.T) {
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
    produces:
      out: exists
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "step run produces unknown artifact out")
}

func TestValidateRejectsUnsupportedProducesCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
artifacts:
  out:
    path: out.txt
steps:
  - id: run
    agent: runner
    prompt: run
    produces:
      out: present
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, "step run produces artifact out has unsupported check present")
}

func TestValidateRejectsUnknownArtifactPromptReference(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
artifacts:
  out:
    path: out.txt
steps:
  - id: run
    agent: runner
    prompt: "write {{ .Artifacts.missing }}"
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, `unknown artifact reference missing`)
}

func TestValidateRejectsUnknownArtifactPromptReferenceInConditional(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
inputs:
  draft:
    type: boolean
agents:
  runner:
    model: gpt-5.1-codex
artifacts:
  out:
    path: out.txt
steps:
  - id: run
    agent: runner
    prompt: '{{ if .Inputs.draft }}write {{ .Artifacts.missing }}{{ end }}'
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, `unknown artifact reference missing`)
}

func TestValidateRejectsArtifactPathHelper(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.yaml")
	writeFile(t, path, `name: sample
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
artifacts:
  out:
    path: out.txt
steps:
  - id: run
    agent: runner
    prompt: '{{ artifact_path "out" }}'
`)

	_, err := LoadFile(path)
	assertErrorContains(t, err, `function "artifact_path" not defined`)
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
