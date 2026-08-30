package acceptance

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	releaseExternalEvidenceSchemaVersion = 1
	releaseExternalEvidenceDirectory     = ".design/acceptance/external"
)

//go:embed release_external_evidence.schema.json
var releaseExternalEvidenceSchema []byte

type releaseExternalEvidenceDefinition struct {
	BehaviorID          string   `json:"behavior_id"`
	CellID              string   `json:"cell_id"`
	TargetOS            string   `json:"target_os"`
	Browser             string   `json:"browser"`
	AssistiveTechnology string   `json:"assistive_technology,omitempty"`
	AcceptanceClass     string   `json:"acceptance_class"`
	UnavailableClass    string   `json:"unavailable_class,omitempty"`
	ExecutableLocator   string   `json:"executable_locator"`
	Checklist           []string `json:"checklist"`
}

type releaseExternalEvidenceBinding struct {
	CandidateHead string
	ProfileHash   string
	ManifestHash  string
}

type releaseEnvironmentProbeResult struct {
	Availability      string
	UnavailableReason string
	ObservedOS        string
	ObservedArch      string
	Executable        string
	ExecutableSHA256  string
}

type releaseEnvironmentProber func(releaseExternalEvidenceDefinition) (releaseEnvironmentProbeResult, error)

type releaseExternalEvidenceTemplate struct {
	Path     string
	Contents []byte
}

type releaseExternalEvidence struct {
	SchemaVersion    int                                `json:"schema_version"`
	BehaviorID       string                             `json:"behavior_id"`
	CellID           string                             `json:"cell_id"`
	CandidateHead    string                             `json:"candidate_head"`
	ProfileHash      string                             `json:"profile_hash"`
	ManifestHash     string                             `json:"manifest_hash"`
	AcceptanceClass  string                             `json:"acceptance_class"`
	UnavailableClass string                             `json:"unavailable_class,omitempty"`
	Probe            releaseExternalEvidenceProbe       `json:"probe"`
	Availability     string                             `json:"availability"`
	Result           string                             `json:"result"`
	Operator         releaseExternalEvidenceOperator    `json:"operator"`
	Environment      releaseExternalEvidenceEnvironment `json:"environment"`
	Checklist        []releaseExternalEvidenceCheck     `json:"checklist"`
	Artifacts        []releaseExternalEvidenceArtifact  `json:"artifacts"`
	Failure          *releaseExternalEvidenceFailure    `json:"failure_handoff,omitempty"`
}

type releaseExternalEvidenceProbe struct {
	Availability      string `json:"availability"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	ObservedOS        string `json:"observed_os"`
	ObservedArch      string `json:"observed_arch"`
}

type releaseExternalEvidenceOperator struct {
	Name string `json:"name"`
}

type releaseExternalEvidenceEnvironment struct {
	OS                         string `json:"os"`
	Browser                    string `json:"browser"`
	BrowserVersion             string `json:"browser_version"`
	AssistiveTechnology        string `json:"assistive_technology,omitempty"`
	AssistiveTechnologyVersion string `json:"assistive_technology_version,omitempty"`
	Executable                 string `json:"executable"`
	ExecutableSHA256           string `json:"executable_sha256"`
}

type releaseExternalEvidenceCheck struct {
	ID     string `json:"id"`
	Result string `json:"result"`
	Notes  string `json:"notes,omitempty"`
}

type releaseExternalEvidenceArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type releaseExternalEvidenceFailure struct {
	Blocking bool   `json:"blocking"`
	Summary  string `json:"summary"`
}

func releaseExternalEvidenceDefinitions(root string) ([]releaseExternalEvidenceDefinition, error) {
	contents, err := os.ReadFile(filepath.Join(root, "mcp-gateway", "web", "environments.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		SchemaVersion     int    `json:"schema_version"`
		PlaywrightVersion string `json:"playwright_version"`
		Cells             []struct {
			ID                  string `json:"id"`
			OS                  string `json:"os"`
			Browser             string `json:"browser"`
			BrowserVersion      string `json:"browser_version,omitempty"`
			AssistiveTechnology string `json:"assistive_technology,omitempty"`
			Availability        string `json:"availability"`
			AcceptanceClass     string `json:"acceptance_class"`
			UnavailableClass    string `json:"unavailable_class,omitempty"`
			Owner               string `json:"owner"`
			ExecutableLocator   string `json:"executable_locator,omitempty"`
			ExecutableSHA256    string `json:"executable_sha256,omitempty"`
		} `json:"cells"`
	}
	if err := strictjson.Decode(contents, &manifest, strictjson.Options{MaxBytes: 32 * 1024, MaxDepth: 6, RejectUnknownMembers: true}); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 1 {
		return nil, errors.New("external environment manifest version is unsupported")
	}
	definitions := canonicalReleaseExternalEvidenceDefinitions()
	cells := make(map[string]struct {
		OS, Browser, AssistiveTechnology, AcceptanceClass, UnavailableClass, ExecutableLocator string
	}, len(manifest.Cells))
	for _, cell := range manifest.Cells {
		cells[cell.ID] = struct {
			OS, Browser, AssistiveTechnology, AcceptanceClass, UnavailableClass, ExecutableLocator string
		}{cell.OS, cell.Browser, cell.AssistiveTechnology, cell.AcceptanceClass, cell.UnavailableClass, cell.ExecutableLocator}
	}
	for index := range definitions {
		cell, ok := cells[definitions[index].CellID]
		if !ok || cell.OS != "macos" || cell.Browser != "safari" || cell.AcceptanceClass != "blocking_when_available" || cell.UnavailableClass != "additive" || cell.ExecutableLocator == "" {
			return nil, fmt.Errorf("external environment cell %s has invalid policy", definitions[index].CellID)
		}
		definitions[index].TargetOS = cell.OS
		definitions[index].Browser = cell.Browser
		definitions[index].AssistiveTechnology = cell.AssistiveTechnology
		definitions[index].AcceptanceClass = cell.AcceptanceClass
		definitions[index].UnavailableClass = cell.UnavailableClass
		definitions[index].ExecutableLocator = cell.ExecutableLocator
	}
	return definitions, nil
}

func canonicalReleaseExternalEvidenceDefinitions() []releaseExternalEvidenceDefinition {
	return []releaseExternalEvidenceDefinition{
		{BehaviorID: "tier.browser.cross", CellID: "macos-safari", TargetOS: "macos", Browser: "safari", AcceptanceClass: "blocking_when_available", UnavailableClass: "additive", ExecutableLocator: "/Applications/Safari.app", Checklist: []string{"sign-in-session", "navigation", "complete-workflows", "responsive-reflow", "network-boundary"}},
		{BehaviorID: "product.accessibility.combined_evidence", CellID: "macos-voiceover-safari", TargetOS: "macos", Browser: "safari", AssistiveTechnology: "voiceover", AcceptanceClass: "blocking_when_available", UnavailableClass: "additive", ExecutableLocator: "/Applications/Safari.app", Checklist: []string{"landmarks-headings", "keyboard-focus", "dialog-announcement", "live-regions", "tables-forms-errors", "zoom-reduced-motion"}},
	}
}

func generateReleaseExternalEvidenceTemplates(binding releaseExternalEvidenceBinding, definitions []releaseExternalEvidenceDefinition, probe releaseEnvironmentProber) ([]releaseExternalEvidenceTemplate, error) {
	if !headPattern.MatchString(binding.CandidateHead) || !digestPattern.MatchString(binding.ProfileHash) || !digestPattern.MatchString(binding.ManifestHash) || probe == nil {
		return nil, errors.New("release external template binding or probe is invalid")
	}
	templates := make([]releaseExternalEvidenceTemplate, 0, len(definitions))
	for _, definition := range definitions {
		if err := validateReleaseExternalDefinition(definition); err != nil {
			return nil, err
		}
		result, err := probe(definition)
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", definition.BehaviorID, err)
		}
		evidence := releaseExternalEvidence{
			SchemaVersion: releaseExternalEvidenceSchemaVersion, BehaviorID: definition.BehaviorID, CellID: definition.CellID,
			CandidateHead: binding.CandidateHead, ProfileHash: binding.ProfileHash, ManifestHash: binding.ManifestHash,
			AcceptanceClass: definition.AcceptanceClass, UnavailableClass: definition.UnavailableClass,
			Probe:        releaseExternalEvidenceProbe{Availability: result.Availability, UnavailableReason: result.UnavailableReason, ObservedOS: result.ObservedOS, ObservedArch: result.ObservedArch},
			Availability: result.Availability, Operator: releaseExternalEvidenceOperator{},
			Environment: releaseExternalEvidenceEnvironment{OS: definition.TargetOS, Browser: definition.Browser, AssistiveTechnology: definition.AssistiveTechnology, Executable: result.Executable, ExecutableSHA256: result.ExecutableSHA256},
			Checklist:   []releaseExternalEvidenceCheck{}, Artifacts: []releaseExternalEvidenceArtifact{},
		}
		switch result.Availability {
		case "available":
			if result.Executable == "" || !digestPattern.MatchString(result.ExecutableSHA256) || result.UnavailableReason != "" {
				return nil, fmt.Errorf("probe %s returned incomplete available provenance", definition.BehaviorID)
			}
			evidence.Result = "pending"
			for _, id := range definition.Checklist {
				evidence.Checklist = append(evidence.Checklist, releaseExternalEvidenceCheck{ID: id, Result: "pending"})
			}
		case "unavailable":
			if definition.AcceptanceClass != "blocking_when_available" || definition.UnavailableClass != "additive" {
				return nil, fmt.Errorf("external evidence %s cannot synthesize unavailability", definition.BehaviorID)
			}
			if !closedUnavailableReason(result.UnavailableReason) || result.Executable != "" || result.ExecutableSHA256 != "" {
				return nil, fmt.Errorf("probe %s returned invalid unavailability", definition.BehaviorID)
			}
			evidence.Result = "unavailable"
		default:
			return nil, fmt.Errorf("probe %s returned unknown availability", definition.BehaviorID)
		}
		contents, err := json.Marshal(evidence)
		if err != nil {
			return nil, err
		}
		if _, err := parseReleaseExternalEvidence(contents, "", binding, definition, true); err != nil {
			return nil, err
		}
		templates = append(templates, releaseExternalEvidenceTemplate{Path: releaseExternalEvidencePath(definition.BehaviorID), Contents: contents})
	}
	return templates, nil
}

func probeReleaseEnvironment(definition releaseExternalEvidenceDefinition, hostOS, hostArch string, resolve func(string) (string, error)) (releaseEnvironmentProbeResult, error) {
	observedOS := hostOS
	if observedOS == "darwin" {
		observedOS = "macos"
	}
	if observedOS != definition.TargetOS {
		return releaseEnvironmentProbeResult{Availability: "unavailable", UnavailableReason: "platform_mismatch", ObservedOS: observedOS, ObservedArch: hostArch}, nil
	}
	if resolve == nil {
		resolve = func(locator string) (string, error) {
			if strings.HasSuffix(locator, ".app") {
				return filepath.Join(locator, "Contents", "MacOS", strings.TrimSuffix(filepath.Base(locator), ".app")), nil
			}
			return locator, nil
		}
	}
	path, err := resolve(definition.ExecutableLocator)
	if errors.Is(err, os.ErrNotExist) {
		return releaseEnvironmentProbeResult{Availability: "unavailable", UnavailableReason: "executable_not_found", ObservedOS: observedOS, ObservedArch: hostArch}, nil
	}
	if err != nil {
		return releaseEnvironmentProbeResult{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return releaseEnvironmentProbeResult{Availability: "unavailable", UnavailableReason: "executable_not_found", ObservedOS: observedOS, ObservedArch: hostArch}, nil
	}
	if err != nil {
		return releaseEnvironmentProbeResult{}, err
	}
	if !info.Mode().IsRegular() {
		return releaseEnvironmentProbeResult{}, errors.New("external executable is not a non-symlink regular file")
	}
	digest, err := sha256File(path)
	if err != nil {
		return releaseEnvironmentProbeResult{}, err
	}
	return releaseEnvironmentProbeResult{Availability: "available", ObservedOS: observedOS, ObservedArch: hostArch, Executable: path, ExecutableSHA256: digest}, nil
}

func defaultReleaseEnvironmentProber(definition releaseExternalEvidenceDefinition) (releaseEnvironmentProbeResult, error) {
	return probeReleaseEnvironment(definition, runtime.GOOS, runtime.GOARCH, nil)
}

func parseReleaseExternalEvidence(contents []byte, artifactRoot string, binding releaseExternalEvidenceBinding, definition releaseExternalEvidenceDefinition, allowPending bool) (releaseExternalEvidence, error) {
	var evidence releaseExternalEvidence
	if err := strictjson.Decode(contents, &evidence, strictjson.Options{MaxBytes: 64 * 1024, MaxDepth: 10, RejectUnknownMembers: true}); err != nil {
		return releaseExternalEvidence{}, err
	}
	if err := validateReleaseExternalSchema(contents); err != nil {
		return releaseExternalEvidence{}, err
	}
	if err := validateReleaseExternalEvidence(evidence, artifactRoot, binding, definition, allowPending); err != nil {
		return releaseExternalEvidence{}, err
	}
	return evidence, nil
}

func validateReleaseExternalSchema(contents []byte) error {
	var document any
	if err := json.Unmarshal(releaseExternalEvidenceSchema, &document); err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:mcp-gateway:release-external-evidence", document); err != nil {
		return err
	}
	schema, err := compiler.Compile("urn:mcp-gateway:release-external-evidence")
	if err != nil {
		return err
	}
	var instance any
	if err := json.Unmarshal(contents, &instance); err != nil {
		return err
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("validate release external evidence schema: %w", err)
	}
	return nil
}

func validateReleaseExternalEvidence(evidence releaseExternalEvidence, artifactRoot string, binding releaseExternalEvidenceBinding, definition releaseExternalEvidenceDefinition, allowPending bool) error {
	if evidence.SchemaVersion != releaseExternalEvidenceSchemaVersion || evidence.BehaviorID != definition.BehaviorID || evidence.CellID != definition.CellID || evidence.AcceptanceClass != definition.AcceptanceClass || evidence.UnavailableClass != definition.UnavailableClass {
		return errors.New("release external evidence does not match its closed definition")
	}
	if evidence.CandidateHead != binding.CandidateHead || evidence.ProfileHash != binding.ProfileHash || evidence.ManifestHash != binding.ManifestHash || !headPattern.MatchString(evidence.CandidateHead) || !digestPattern.MatchString(evidence.ProfileHash) || !digestPattern.MatchString(evidence.ManifestHash) {
		return errors.New("release external evidence is not bound to the exact candidate")
	}
	if evidence.Probe.Availability != evidence.Availability || evidence.Probe.ObservedOS == "" || evidence.Probe.ObservedArch == "" {
		return errors.New("release external probe provenance is incomplete")
	}
	if evidence.Environment.OS != definition.TargetOS || evidence.Environment.Browser != definition.Browser || evidence.Environment.AssistiveTechnology != definition.AssistiveTechnology {
		return errors.New("release external environment does not match its definition")
	}
	if evidence.Availability == "unavailable" {
		if evidence.Result != "unavailable" || definition.UnavailableClass != "additive" || !closedUnavailableReason(evidence.Probe.UnavailableReason) || evidence.Operator.Name != "" || evidence.Environment.BrowserVersion != "" || evidence.Environment.Executable != "" || evidence.Environment.ExecutableSHA256 != "" || len(evidence.Checklist) != 0 || len(evidence.Artifacts) != 0 || evidence.Failure != nil {
			return errors.New("unavailable release external evidence invents results or provenance")
		}
		return nil
	}
	if evidence.Availability != "available" || evidence.Probe.UnavailableReason != "" || evidence.Environment.Executable == "" || !digestPattern.MatchString(evidence.Environment.ExecutableSHA256) {
		return errors.New("available release external evidence has incomplete executable provenance")
	}
	if evidence.Result == "pending" {
		if !allowPending || evidence.Operator.Name != "" || evidence.Environment.BrowserVersion != "" || len(evidence.Artifacts) != 0 || evidence.Failure != nil {
			return errors.New("pending release external evidence cannot be qualified")
		}
		return validateReleaseExternalChecklist(evidence.Checklist, definition.Checklist, "pending")
	}
	if evidence.Result != "passed" && evidence.Result != "failed" {
		return errors.New("available release external evidence must pass or fail")
	}
	if strings.TrimSpace(evidence.Operator.Name) == "" || strings.TrimSpace(evidence.Environment.BrowserVersion) == "" {
		return errors.New("qualified release external evidence requires named operator and browser provenance")
	}
	if definition.AssistiveTechnology != "" && evidence.Environment.AssistiveTechnologyVersion == "" {
		return errors.New("assistive evidence requires technology version provenance")
	}
	expectedResult := "passed"
	if evidence.Result == "failed" {
		expectedResult = "mixed"
		if evidence.Failure == nil || !evidence.Failure.Blocking || strings.TrimSpace(evidence.Failure.Summary) == "" {
			return errors.New("failed release external evidence requires blocking handoff")
		}
	} else if evidence.Failure != nil {
		return errors.New("passed release external evidence cannot include failure handoff")
	}
	if err := validateReleaseExternalChecklist(evidence.Checklist, definition.Checklist, expectedResult); err != nil {
		return err
	}
	if len(evidence.Artifacts) == 0 {
		return errors.New("qualified release external evidence requires artifacts")
	}
	seen := make(map[string]struct{}, len(evidence.Artifacts))
	prefix := filepath.ToSlash(filepath.Join("artifacts", definition.BehaviorID)) + "/"
	for _, artifact := range evidence.Artifacts {
		if err := validateReleaseRelativePath(artifact.Path); err != nil || strings.Contains(artifact.Path, `\`) || !strings.HasPrefix(artifact.Path, prefix) || !digestPattern.MatchString(artifact.SHA256) {
			return errors.New("release external artifact path or digest is invalid")
		}
		if _, duplicate := seen[artifact.Path]; duplicate {
			return errors.New("release external artifact is duplicated")
		}
		seen[artifact.Path] = struct{}{}
		if artifactRoot == "" {
			return errors.New("release external artifact root is required")
		}
		absolute := filepath.Join(artifactRoot, filepath.FromSlash(artifact.Path))
		info, err := os.Lstat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("release external artifact is missing or not regular")
		}
		digest, err := sha256File(absolute)
		if err != nil || digest != artifact.SHA256 {
			return errors.New("release external artifact digest does not match")
		}
	}
	return nil
}

func validateReleaseExternalChecklist(actual []releaseExternalEvidenceCheck, expected []string, mode string) error {
	if len(actual) != len(expected) {
		return errors.New("release external checklist is incomplete")
	}
	failed := 0
	for index, item := range actual {
		if item.ID != expected[index] {
			return errors.New("release external checklist order or identity changed")
		}
		switch mode {
		case "pending", "passed":
			if item.Result != mode || item.Notes != "" {
				return errors.New("release external checklist result is invalid")
			}
		case "mixed":
			if item.Result != "passed" && item.Result != "failed" {
				return errors.New("release external checklist result is invalid")
			}
			if item.Result == "failed" {
				failed++
				if strings.TrimSpace(item.Notes) == "" {
					return errors.New("failed release external checklist item requires notes")
				}
			}
		}
	}
	if mode == "mixed" && failed == 0 {
		return errors.New("failed release external evidence requires a failed checklist item")
	}
	return nil
}

func loadReleaseExternalEvidence(root string, binding releaseExternalEvidenceBinding, definitions []releaseExternalEvidenceDefinition) ([]releaseExternalEvidenceReference, error) {
	directory := filepath.Join(root, releaseExternalEvidenceDirectory)
	references := make([]releaseExternalEvidenceReference, 0, len(definitions))
	for _, definition := range definitions {
		relative := releaseExternalEvidencePath(definition.BehaviorID)
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("release external sidecar %s is missing or not regular", relative)
		}
		contents, err := os.ReadFile(path) //nolint:gosec // Closed behavior-derived sidecar path under the selected repository root.
		if err != nil {
			return nil, err
		}
		evidence, err := parseReleaseExternalEvidence(contents, directory, binding, definition, false)
		if err != nil {
			return nil, fmt.Errorf("validate release external sidecar %s: %w", relative, err)
		}
		if evidence.Result == "failed" || evidence.Result == "pending" {
			return nil, fmt.Errorf("release external sidecar %s is not qualified", relative)
		}
		references = append(references, releaseExternalEvidenceReference{
			ID: definition.BehaviorID, CellID: definition.CellID, Availability: evidence.Availability, Result: evidence.Result,
			Blocking: evidence.Result != "unavailable", Path: relative, SHA256: releaseDigest(contents),
		})
	}
	return references, nil
}

func qualifyReleaseExternalEvidence(ctx context.Context, root string, profile releaseProfileDefinition) ([]releaseExternalEvidenceReference, error) {
	revision, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return nil, errors.New("release external qualification cannot resolve HEAD")
	}
	status, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil || status != "" {
		return nil, errors.New("release external qualification requires a clean workspace")
	}
	profileHash, _, manifestHash, err := releaseDefinitionHashes(root, profile)
	if err != nil {
		return nil, err
	}
	binding := releaseExternalEvidenceBinding{CandidateHead: revision, ProfileHash: profileHash, ManifestHash: manifestHash}
	references, err := loadReleaseExternalEvidence(root, binding, profile.ExternalEvidence)
	if err != nil {
		return nil, err
	}
	if err := validateReleaseExternalEvidenceReferences(root, binding, profile.ExternalEvidence, references); err != nil {
		return nil, err
	}
	finalRevision, revisionErr := gitOutput(ctx, root, "rev-parse", "HEAD")
	finalStatus, statusErr := gitOutput(ctx, root, "status", "--porcelain")
	if revisionErr != nil || statusErr != nil || finalRevision != revision || finalStatus != "" {
		return nil, errors.New("release external qualification changed candidate state")
	}
	return references, nil
}

func validateReleaseExternalEvidenceReferences(root string, binding releaseExternalEvidenceBinding, definitions []releaseExternalEvidenceDefinition, references []releaseExternalEvidenceReference) error {
	if err := validateReleaseExternalReferenceDefinitions(definitions, references); err != nil {
		return err
	}
	loaded, err := loadReleaseExternalEvidence(root, binding, definitions)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(loaded, references) {
		return errors.New("release external evidence references changed or are out of order")
	}
	return nil
}

func validateReleaseExternalReferenceDefinitions(definitions []releaseExternalEvidenceDefinition, references []releaseExternalEvidenceReference) error {
	if len(references) != len(definitions) {
		return errors.New("release report requires the exact external evidence set")
	}
	for index, reference := range references {
		definition := definitions[index]
		if reference.ID != definition.BehaviorID || reference.CellID != definition.CellID || reference.Path != releaseExternalEvidencePath(definition.BehaviorID) || !digestPattern.MatchString(reference.SHA256) {
			return errors.New("release external evidence reference is not closed or digest-bound")
		}
		available := reference.Availability == "available" && (reference.Result == "passed" || reference.Result == "failed") && reference.Blocking
		unavailable := reference.Availability == "unavailable" && reference.Result == "unavailable" && !reference.Blocking && definition.UnavailableClass == "additive"
		if !available && !unavailable {
			return errors.New("release external evidence reference classification is invalid")
		}
	}
	return nil
}

func releaseExternalEvidencePath(behaviorID string) string {
	return filepath.ToSlash(filepath.Join(releaseExternalEvidenceDirectory, behaviorID+".json"))
}

func validateReleaseExternalDefinition(definition releaseExternalEvidenceDefinition) error {
	if _, ok := releaseBehaviorIDs()[definition.BehaviorID]; !ok || definition.CellID == "" || definition.TargetOS == "" || definition.Browser == "" || definition.ExecutableLocator == "" || len(definition.Checklist) == 0 {
		return fmt.Errorf("release external definition %s is incomplete", definition.BehaviorID)
	}
	if definition.AcceptanceClass != "blocking" && definition.AcceptanceClass != "blocking_when_available" {
		return fmt.Errorf("release external definition %s has invalid acceptance class", definition.BehaviorID)
	}
	seen := make(map[string]struct{}, len(definition.Checklist))
	for _, id := range definition.Checklist {
		if id == "" {
			return errors.New("release external checklist contains an empty item")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("release external checklist contains a duplicate item")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func closedUnavailableReason(reason string) bool {
	return reason == "platform_mismatch" || reason == "executable_not_found" || reason == "assistive_technology_unavailable"
}

var headPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
