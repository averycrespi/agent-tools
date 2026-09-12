package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseExternalEvidenceDefinitionsTrackEnvironmentManifest(t *testing.T) {
	definitions, err := releaseExternalEvidenceDefinitions(repositoryRoot(t))
	require.NoError(t, err)
	assert.Equal(t, releaseExternalTestDefinitions(), definitions)
	result, err := defaultReleaseEnvironmentProber(definitions[0])
	require.NoError(t, err)
	if runtime.GOOS != "darwin" {
		assert.Equal(t, "platform_mismatch", result.UnavailableReason)
	}
}

func TestReleaseExternalEvidenceTemplatesAreDeterministicAndNeverPass(t *testing.T) {
	definitions := releaseExternalTestDefinitions()
	binding := releaseExternalEvidenceBinding{CandidateHead: strings.Repeat("a", 40), ProfileHash: "sha256:" + strings.Repeat("b", 64), ManifestHash: "sha256:" + strings.Repeat("c", 64)}
	available := func(releaseExternalEvidenceDefinition) (releaseEnvironmentProbeResult, error) {
		return releaseEnvironmentProbeResult{Availability: "available", ObservedOS: "macos", ObservedArch: "arm64", Executable: "/Applications/Safari.app/Contents/MacOS/Safari", ExecutableSHA256: "sha256:" + strings.Repeat("d", 64)}, nil
	}
	first, err := generateReleaseExternalEvidenceTemplates(binding, definitions, available)
	require.NoError(t, err)
	second, err := generateReleaseExternalEvidenceTemplates(binding, definitions, available)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	require.Len(t, first, 2)
	for index, template := range first {
		assert.Equal(t, releaseExternalEvidencePath(definitions[index].BehaviorID), template.Path)
		var evidence releaseExternalEvidence
		require.NoError(t, json.Unmarshal(template.Contents, &evidence))
		assert.Equal(t, "available", evidence.Availability)
		assert.Equal(t, "pending", evidence.Result)
		assert.Empty(t, evidence.Operator.Name)
		assert.Empty(t, evidence.Artifacts)
		for _, check := range evidence.Checklist {
			assert.Equal(t, "pending", check.Result)
		}
	}
}

func TestReleaseExternalEvidenceUnavailablePolicyIsClosed(t *testing.T) {
	definitions := releaseExternalTestDefinitions()
	binding := releaseExternalEvidenceBinding{CandidateHead: strings.Repeat("a", 40), ProfileHash: "sha256:" + strings.Repeat("b", 64), ManifestHash: "sha256:" + strings.Repeat("c", 64)}
	unavailable := func(releaseExternalEvidenceDefinition) (releaseEnvironmentProbeResult, error) {
		return releaseEnvironmentProbeResult{Availability: "unavailable", UnavailableReason: "platform_mismatch", ObservedOS: "linux", ObservedArch: "amd64"}, nil
	}
	templates, err := generateReleaseExternalEvidenceTemplates(binding, definitions, unavailable)
	require.NoError(t, err)
	for _, template := range templates {
		var evidence releaseExternalEvidence
		require.NoError(t, json.Unmarshal(template.Contents, &evidence))
		assert.Equal(t, "unavailable", evidence.Result)
		assert.Empty(t, evidence.Checklist)
		assert.Empty(t, evidence.Artifacts)
	}

	blocking := append([]releaseExternalEvidenceDefinition(nil), definitions...)
	blocking[0].UnavailableClass = ""
	_, err = generateReleaseExternalEvidenceTemplates(binding, blocking, unavailable)
	assert.ErrorContains(t, err, "cannot synthesize")
	_, err = generateReleaseExternalEvidenceTemplates(binding, definitions, func(releaseExternalEvidenceDefinition) (releaseEnvironmentProbeResult, error) {
		return releaseEnvironmentProbeResult{}, errors.New("permission denied")
	})
	assert.ErrorContains(t, err, "permission denied")
}

func TestReleaseEnvironmentProbeClassifiesOnlyClosedUnavailableStates(t *testing.T) {
	definition := releaseExternalTestDefinitions()[0]
	result, err := probeReleaseEnvironment(definition, "linux", "amd64", nil)
	require.NoError(t, err)
	assert.Equal(t, "unavailable", result.Availability)
	assert.Equal(t, "platform_mismatch", result.UnavailableReason)

	result, err = probeReleaseEnvironment(definition, "darwin", "arm64", func(string) (string, error) { return "", os.ErrNotExist })
	require.NoError(t, err)
	assert.Equal(t, "executable_not_found", result.UnavailableReason)
	_, err = probeReleaseEnvironment(definition, "darwin", "arm64", func(string) (string, error) { return "", errors.New("permission denied") })
	assert.ErrorContains(t, err, "permission denied")
}

func TestReleaseExternalEvidenceRejectsCandidateArtifactAndLegacyPathDrift(t *testing.T) {
	root := t.TempDir()
	definition := releaseExternalTestDefinitions()[0]
	binding := releaseExternalEvidenceBinding{CandidateHead: strings.Repeat("a", 40), ProfileHash: "sha256:" + strings.Repeat("b", 64), ManifestHash: "sha256:" + strings.Repeat("c", 64)}
	artifact := filepath.Join(root, "artifacts", definition.BehaviorID, "capture.png")
	require.NoError(t, os.MkdirAll(filepath.Dir(artifact), 0o700))
	require.NoError(t, os.WriteFile(artifact, []byte("sanitized capture"), 0o600))
	digest, err := sha256File(artifact)
	require.NoError(t, err)
	evidence := passedReleaseExternalEvidence(definition, binding, filepath.ToSlash(filepath.Join("artifacts", definition.BehaviorID, "capture.png")), digest)
	contents, err := json.Marshal(evidence)
	require.NoError(t, err)
	_, err = parseReleaseExternalEvidence(contents, root, binding, definition, false)
	require.NoError(t, err)

	drifted := evidence
	drifted.CandidateHead = strings.Repeat("d", 40)
	contents, err = json.Marshal(drifted)
	require.NoError(t, err)
	_, err = parseReleaseExternalEvidence(contents, root, binding, definition, false)
	assert.ErrorContains(t, err, "exact candidate")

	drifted = evidence
	drifted.Artifacts[0].SHA256 = "sha256:" + strings.Repeat("0", 64)
	contents, err = json.Marshal(drifted)
	require.NoError(t, err)
	_, err = parseReleaseExternalEvidence(contents, root, binding, definition, false)
	assert.ErrorContains(t, err, "artifact digest")

	err = validateReleaseExternalEvidenceReferences(root, binding, []releaseExternalEvidenceDefinition{definition}, []releaseExternalEvidenceReference{{ID: definition.BehaviorID, CellID: definition.CellID, Availability: "available", Result: "passed", Blocking: true, Path: ".design/acceptance/s6/external/real-safari.json", SHA256: releaseDigest(contents)}})
	assert.ErrorIs(t, err, errLegacyExternalEvidence)
}

func TestReleaseExternalEvidenceQualifierRequiresExactCleanReferenceSet(t *testing.T) {
	root, profile := releaseExternalTestRepository(t)
	profileHash, _, manifestHash, err := releaseDefinitionHashes(root, profile)
	require.NoError(t, err)
	revision, err := gitOutput(t.Context(), root, "rev-parse", "HEAD")
	require.NoError(t, err)
	binding := releaseExternalEvidenceBinding{CandidateHead: revision, ProfileHash: profileHash, ManifestHash: manifestHash}
	templates, err := generateReleaseExternalEvidenceTemplates(binding, profile.ExternalEvidence, func(releaseExternalEvidenceDefinition) (releaseEnvironmentProbeResult, error) {
		return releaseEnvironmentProbeResult{Availability: "unavailable", UnavailableReason: "platform_mismatch", ObservedOS: "linux", ObservedArch: "amd64"}, nil
	})
	require.NoError(t, err)
	for _, template := range templates {
		path := filepath.Join(root, filepath.FromSlash(template.Path))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, template.Contents, 0o600))
	}
	references, err := qualifyReleaseExternalEvidence(context.Background(), root, profile)
	require.NoError(t, err)
	require.Len(t, references, 2)
	for _, reference := range references {
		assert.Equal(t, "unavailable", reference.Result)
		assert.False(t, reference.Blocking)
		assert.NotEmpty(t, reference.Path)
		assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, reference.SHA256)
	}
	require.NoError(t, validateReleaseExternalEvidenceReferences(root, binding, profile.ExternalEvidence, references))
	assert.Error(t, validateReleaseExternalEvidenceReferences(root, binding, profile.ExternalEvidence, references[:1]))

	require.NoError(t, os.WriteFile(filepath.Join(root, "dirty"), []byte("dirty"), 0o600))
	_, err = qualifyReleaseExternalEvidence(context.Background(), root, profile)
	assert.ErrorContains(t, err, "clean workspace")
}

func releaseExternalTestDefinitions() []releaseExternalEvidenceDefinition {
	return []releaseExternalEvidenceDefinition{
		{BehaviorID: "tier.browser.cross", CellID: "macos-safari", TargetOS: "macos", Browser: "safari", AcceptanceClass: "blocking_when_available", UnavailableClass: "additive", ExecutableLocator: "/Applications/Safari.app", Checklist: []string{"sign-in-session", "navigation", "complete-workflows", "responsive-reflow", "network-boundary"}},
		{BehaviorID: "product.accessibility.combined_evidence", CellID: "macos-voiceover-safari", TargetOS: "macos", Browser: "safari", AssistiveTechnology: "voiceover", AcceptanceClass: "blocking_when_available", UnavailableClass: "additive", ExecutableLocator: "/Applications/Safari.app", Checklist: []string{"landmarks-headings", "keyboard-focus", "dialog-announcement", "live-regions", "tables-forms-errors", "zoom-reduced-motion"}},
	}
}

func passedReleaseExternalEvidence(definition releaseExternalEvidenceDefinition, binding releaseExternalEvidenceBinding, artifactPath, artifactDigest string) releaseExternalEvidence {
	checks := make([]releaseExternalEvidenceCheck, len(definition.Checklist))
	for index, id := range definition.Checklist {
		checks[index] = releaseExternalEvidenceCheck{ID: id, Result: "passed"}
	}
	return releaseExternalEvidence{SchemaVersion: releaseExternalEvidenceSchemaVersion, BehaviorID: definition.BehaviorID, CellID: definition.CellID, CandidateHead: binding.CandidateHead, ProfileHash: binding.ProfileHash, ManifestHash: binding.ManifestHash, AcceptanceClass: definition.AcceptanceClass, UnavailableClass: definition.UnavailableClass, Probe: releaseExternalEvidenceProbe{Availability: "available", ObservedOS: "macos", ObservedArch: "arm64"}, Availability: "available", Result: "passed", Operator: releaseExternalEvidenceOperator{Name: "Named operator"}, Environment: releaseExternalEvidenceEnvironment{OS: "macos", Browser: definition.Browser, BrowserVersion: "18.0", Executable: "/Applications/Safari.app/Contents/MacOS/Safari", ExecutableSHA256: "sha256:" + strings.Repeat("d", 64)}, Checklist: checks, Artifacts: []releaseExternalEvidenceArtifact{{Path: artifactPath, SHA256: artifactDigest}}}
}

func releaseExternalTestRepository(t *testing.T) (string, releaseProfileDefinition) {
	t.Helper()
	root, profile := releaseReportTestRepository(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".design/acceptance/external/\n"), 0o600))
	runGit(t, root, "add", ".gitignore")
	runGit(t, root, "-c", "user.name=Acceptance Test", "-c", "user.email=acceptance@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "ignore evidence")
	profile.ExternalEvidence = releaseExternalTestDefinitions()
	return root, profile
}
