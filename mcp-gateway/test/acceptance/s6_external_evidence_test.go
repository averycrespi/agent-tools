package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6ExternalEvidenceContract(t *testing.T) {
	root := filepath.Join(repositoryRoot(t), "mcp-gateway")
	manifestData, err := os.ReadFile(filepath.Join(root, "web", "environments.json"))
	require.NoError(t, err)
	resolve := func(locator string) (string, error) {
		browser := strings.TrimPrefix(locator, "playwright:")
		command := exec.Command("node", "-e", `const {chromium,firefox,webkit}=require("@playwright/test"); process.stdout.write(({chromium,firefox,webkit})[process.argv[1]].executablePath())`, browser)
		command.Dir = filepath.Dir(root)
		output, runErr := command.Output()
		return string(output), runErr
	}
	manifest, err := DecodeAndValidateS6EnvironmentManifest(manifestData, resolve)
	require.NoError(t, err)
	require.Len(t, manifest.Cells, 5)
	assert.Equal(t, "blocking", manifest.Cells[0].AcceptanceClass)
	assert.Equal(t, "additive", manifest.Cells[4].UnavailableClass)

	var invalidManifest map[string]any
	require.NoError(t, json.Unmarshal(manifestData, &invalidManifest))
	cells := invalidManifest["cells"].([]any)
	cells[0].(map[string]any)["executable_sha256"] = "sha256:" + strings.Repeat("0", 64)
	encoded, err := json.Marshal(invalidManifest)
	require.NoError(t, err)
	_, err = DecodeAndValidateS6EnvironmentManifest(encoded, resolve)
	assert.ErrorContains(t, err, "digest mismatch")
	cells[0].(map[string]any)["availability"] = "silently_skipped"
	encoded, err = json.Marshal(invalidManifest)
	require.NoError(t, err)
	_, err = DecodeAndValidateS6EnvironmentManifest(encoded, resolve)
	assert.Error(t, err)

	artifactRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(artifactRoot, "capture.png"), []byte("sanitized-external-artifact"), 0o600))
	artifactDigest, err := sha256File(filepath.Join(artifactRoot, "capture.png"))
	require.NoError(t, err)
	binding := S6EvidenceBinding{
		CandidateHead: strings.Repeat("a", 40),
		ManifestHash:  "sha256:" + strings.Repeat("b", 64),
		ProfileHash:   "sha256:" + strings.Repeat("c", 64),
	}
	valid := evidenceFixture("real_safari", binding, artifactDigest)
	encoded, err = json.Marshal(valid)
	require.NoError(t, err)
	decoded, err := DecodeAndValidateS6ExternalEvidence(encoded, artifactRoot, binding)
	require.NoError(t, err)
	assert.Equal(t, "passed", decoded.Result)

	assistive := evidenceFixture("manual_assistive", binding, artifactDigest)
	assistive.CellID = "macos-voiceover-safari"
	assistive.Environment.AssistiveTech = "voiceover"
	assistive.Environment.AssistiveVersion = "macOS 15"
	encoded, err = json.Marshal(assistive)
	require.NoError(t, err)
	_, err = DecodeAndValidateS6ExternalEvidence(encoded, artifactRoot, binding)
	require.NoError(t, err)

	failed := evidenceFixture("real_safari", binding, artifactDigest)
	failed.Result = "failed"
	failed.Checklist[0].Result = "failed"
	failed.Checklist[0].Notes = "The session recovery check failed at the named step."
	failed.Failure = &S6EvidenceFailure{Blocking: true, Summary: "Real Safari qualification failed; return to a narrow reproducer."}
	encoded, err = json.Marshal(failed)
	require.NoError(t, err)
	_, err = DecodeAndValidateS6ExternalEvidence(encoded, artifactRoot, binding)
	require.NoError(t, err)

	unavailable := evidenceFixture("real_safari", binding, artifactDigest)
	unavailable.Availability = "unavailable_additive"
	unavailable.Result = "unavailable"
	unavailable.Environment.BrowserVersion = ""
	unavailable.Environment.Executable = ""
	unavailable.Environment.ExecutableSHA256 = ""
	unavailable.Checklist = nil
	unavailable.Artifacts = nil
	unavailable.Failure = &S6EvidenceFailure{Blocking: false, Summary: "No named macOS operator environment is available."}
	encoded, err = json.Marshal(unavailable)
	require.NoError(t, err)
	_, err = DecodeAndValidateS6ExternalEvidence(encoded, artifactRoot, binding)
	require.NoError(t, err)

	for name, mutate := range map[string]func(*S6ExternalEvidence){
		"wrong revision":         func(value *S6ExternalEvidence) { value.CandidateHead = strings.Repeat("d", 40) },
		"missing checklist":      func(value *S6ExternalEvidence) { value.Checklist = value.Checklist[1:] },
		"bad artifact":           func(value *S6ExternalEvidence) { value.Artifacts[0].SHA256 = "sha256:" + strings.Repeat("0", 64) },
		"failed without handoff": func(value *S6ExternalEvidence) { value.Result = "failed" },
		"traversal":              func(value *S6ExternalEvidence) { value.Artifacts[0].Path = "../capture.png" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := evidenceFixture("real_safari", binding, artifactDigest)
			mutate(&candidate)
			data, marshalErr := json.Marshal(candidate)
			require.NoError(t, marshalErr)
			_, validationErr := DecodeAndValidateS6ExternalEvidence(data, artifactRoot, binding)
			assert.Error(t, validationErr)
		})
	}
	encoded, err = json.Marshal(valid)
	require.NoError(t, err)
	encoded = append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
	_, err = DecodeAndValidateS6ExternalEvidence(encoded, artifactRoot, binding)
	assert.ErrorContains(t, err, "unknown field")
}

func evidenceFixture(kind string, binding S6EvidenceBinding, artifactDigest string) S6ExternalEvidence {
	checks := make([]S6EvidenceCheck, 0, len(s6Checklists[kind]))
	for _, id := range s6Checklists[kind] {
		checks = append(checks, S6EvidenceCheck{ID: id, Result: "passed"})
	}
	return S6ExternalEvidence{
		SchemaVersion: S6ExternalEvidenceSchemaVersion,
		Kind:          kind,
		CellID:        "macos-safari",
		CandidateHead: binding.CandidateHead,
		ManifestHash:  binding.ManifestHash,
		ProfileHash:   binding.ProfileHash,
		Operator:      S6EvidenceOperator{Name: "Named external operator"},
		Environment: S6EvidenceEnvironment{
			OS: "macos", Browser: "safari", BrowserVersion: "18.0", Executable: "/Applications/Safari.app", ExecutableSHA256: "sha256:" + strings.Repeat("e", 64),
		},
		Availability: "available",
		Result:       "passed",
		Checklist:    checks,
		Artifacts:    []S6EvidenceArtifact{{Path: "capture.png", SHA256: artifactDigest}},
	}
}
