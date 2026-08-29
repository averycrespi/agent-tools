package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const S6ExternalEvidenceSchemaVersion = 1

var (
	s6SHA256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	s6HeadPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	s6Checklists    = map[string][]string{
		"real_safari":      {"sign-in-session", "navigation", "complete-workflows", "responsive-reflow", "network-boundary"},
		"manual_assistive": {"landmarks-headings", "keyboard-focus", "dialog-announcement", "live-regions", "tables-forms-errors", "zoom-reduced-motion"},
	}
)

type S6EnvironmentManifest struct {
	SchemaVersion     int                 `json:"schema_version"`
	PlaywrightVersion string              `json:"playwright_version"`
	Cells             []S6EnvironmentCell `json:"cells"`
}

type S6EnvironmentCell struct {
	ID                string `json:"id"`
	OS                string `json:"os"`
	Browser           string `json:"browser"`
	BrowserVersion    string `json:"browser_version,omitempty"`
	AssistiveTech     string `json:"assistive_technology,omitempty"`
	Availability      string `json:"availability"`
	AcceptanceClass   string `json:"acceptance_class"`
	UnavailableClass  string `json:"unavailable_class,omitempty"`
	Owner             string `json:"owner"`
	ExecutableLocator string `json:"executable_locator,omitempty"`
	ExecutableSHA256  string `json:"executable_sha256,omitempty"`
}

type S6ExecutableResolver func(locator string) (string, error)

func DecodeAndValidateS6EnvironmentManifest(data []byte, resolve S6ExecutableResolver) (S6EnvironmentManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest S6EnvironmentManifest
	if err := decoder.Decode(&manifest); err != nil {
		return S6EnvironmentManifest{}, fmt.Errorf("decode environment manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return S6EnvironmentManifest{}, err
	}
	expected := []string{"linux-chromium", "linux-firefox", "linux-playwright-webkit", "macos-safari", "macos-voiceover-safari"}
	if manifest.SchemaVersion != 1 || manifest.PlaywrightVersion != "1.62.1" || len(manifest.Cells) != len(expected) {
		return S6EnvironmentManifest{}, errors.New("environment manifest shape is invalid")
	}
	for index, cell := range manifest.Cells {
		if cell.ID != expected[index] || cell.Owner == "" || cell.OS == "" || cell.Browser == "" {
			return S6EnvironmentManifest{}, errors.New("environment cell identity is invalid")
		}
		if cell.Availability == "available" {
			if cell.AcceptanceClass != "blocking" && cell.AcceptanceClass != "blocking_when_available" {
				return S6EnvironmentManifest{}, errors.New("available environment cell is not blocking")
			}
			if cell.BrowserVersion == "" || cell.ExecutableLocator == "" || !s6SHA256Pattern.MatchString(cell.ExecutableSHA256) || resolve == nil {
				return S6EnvironmentManifest{}, errors.New("available environment provenance is incomplete")
			}
			path, err := resolve(cell.ExecutableLocator)
			if err != nil {
				return S6EnvironmentManifest{}, fmt.Errorf("preflight %s: %w", cell.ID, err)
			}
			digest, err := sha256File(path)
			if err != nil || digest != cell.ExecutableSHA256 {
				return S6EnvironmentManifest{}, fmt.Errorf("preflight %s: executable digest mismatch", cell.ID)
			}
			continue
		}
		if cell.Availability != "unavailable_on_linux_runner" || cell.AcceptanceClass != "blocking_when_available" || cell.UnavailableClass != "additive" || cell.ExecutableSHA256 != "" {
			return S6EnvironmentManifest{}, errors.New("unavailable environment cell classification is invalid")
		}
	}
	return manifest, nil
}

type S6EvidenceBinding struct {
	CandidateHead string
	ManifestHash  string
	ProfileHash   string
}

type S6ExternalEvidence struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	CellID        string                `json:"cell_id"`
	CandidateHead string                `json:"candidate_head"`
	ManifestHash  string                `json:"manifest_hash"`
	ProfileHash   string                `json:"profile_hash"`
	Operator      S6EvidenceOperator    `json:"operator"`
	Environment   S6EvidenceEnvironment `json:"environment"`
	Availability  string                `json:"availability"`
	Result        string                `json:"result"`
	Checklist     []S6EvidenceCheck     `json:"checklist"`
	Artifacts     []S6EvidenceArtifact  `json:"artifacts"`
	Failure       *S6EvidenceFailure    `json:"failure_handoff"`
}

type S6EvidenceOperator struct {
	Name string `json:"name"`
}

type S6EvidenceEnvironment struct {
	OS               string `json:"os"`
	Browser          string `json:"browser"`
	BrowserVersion   string `json:"browser_version"`
	AssistiveTech    string `json:"assistive_technology,omitempty"`
	AssistiveVersion string `json:"assistive_technology_version,omitempty"`
	Executable       string `json:"executable"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

type S6EvidenceCheck struct {
	ID     string `json:"id"`
	Result string `json:"result"`
	Notes  string `json:"notes,omitempty"`
}

type S6EvidenceArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type S6EvidenceFailure struct {
	Blocking bool   `json:"blocking"`
	Summary  string `json:"summary"`
}

func DecodeAndValidateS6ExternalEvidence(data []byte, artifactRoot string, binding S6EvidenceBinding) (S6ExternalEvidence, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence S6ExternalEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return S6ExternalEvidence{}, fmt.Errorf("decode external evidence: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return S6ExternalEvidence{}, err
	}
	if err := validateS6ExternalEvidence(evidence, artifactRoot, binding); err != nil {
		return S6ExternalEvidence{}, err
	}
	return evidence, nil
}

func validateS6ExternalEvidence(evidence S6ExternalEvidence, artifactRoot string, binding S6EvidenceBinding) error {
	expectedChecks, knownKind := s6Checklists[evidence.Kind]
	if evidence.SchemaVersion != S6ExternalEvidenceSchemaVersion || !knownKind {
		return errors.New("external evidence schema or kind is unsupported")
	}
	if evidence.CandidateHead != binding.CandidateHead || evidence.ManifestHash != binding.ManifestHash || evidence.ProfileHash != binding.ProfileHash || !s6HeadPattern.MatchString(evidence.CandidateHead) || !s6SHA256Pattern.MatchString(evidence.ManifestHash) || !s6SHA256Pattern.MatchString(evidence.ProfileHash) {
		return errors.New("external evidence is not bound to the exact candidate")
	}
	if strings.TrimSpace(evidence.CellID) == "" || strings.TrimSpace(evidence.Operator.Name) == "" || strings.TrimSpace(evidence.Environment.OS) == "" || strings.TrimSpace(evidence.Environment.Browser) == "" {
		return errors.New("external evidence provenance is incomplete")
	}
	if evidence.Availability == "unavailable_additive" {
		if evidence.Result != "unavailable" || len(evidence.Checklist) != 0 || len(evidence.Artifacts) != 0 || evidence.Failure == nil || evidence.Failure.Blocking || strings.TrimSpace(evidence.Failure.Summary) == "" {
			return errors.New("unavailable external evidence requires an additive handoff without invented results")
		}
		return nil
	}
	if strings.TrimSpace(evidence.Environment.BrowserVersion) == "" || strings.TrimSpace(evidence.Environment.Executable) == "" || !s6SHA256Pattern.MatchString(evidence.Environment.ExecutableSHA256) {
		return errors.New("available external evidence executable provenance is incomplete")
	}
	if evidence.Kind == "manual_assistive" && (evidence.Environment.AssistiveTech == "" || evidence.Environment.AssistiveVersion == "") {
		return errors.New("manual assistive evidence requires named technology provenance")
	}
	if evidence.Availability != "available" || (evidence.Result != "passed" && evidence.Result != "failed") {
		return errors.New("available external evidence must report passed or failed")
	}
	if evidence.Result == "failed" {
		if evidence.Failure == nil || !evidence.Failure.Blocking || strings.TrimSpace(evidence.Failure.Summary) == "" {
			return errors.New("failed external evidence requires a blocking handoff")
		}
	} else if evidence.Failure != nil {
		return errors.New("passed external evidence cannot include a failure handoff")
	}
	actualChecks := make([]string, 0, len(evidence.Checklist))
	failedChecks := 0
	for _, check := range evidence.Checklist {
		if check.Result != "passed" && check.Result != "failed" {
			return errors.New("external checklist result is invalid")
		}
		if check.Result == "failed" {
			failedChecks++
			if strings.TrimSpace(check.Notes) == "" {
				return errors.New("failed external checklist item requires notes")
			}
		}
		actualChecks = append(actualChecks, check.ID)
	}
	sort.Strings(actualChecks)
	expectedChecks = append([]string(nil), expectedChecks...)
	sort.Strings(expectedChecks)
	if strings.Join(actualChecks, "\x00") != strings.Join(expectedChecks, "\x00") {
		return errors.New("external evidence checklist is incomplete or duplicated")
	}
	if (evidence.Result == "passed" && failedChecks != 0) || (evidence.Result == "failed" && failedChecks == 0) {
		return errors.New("external evidence result does not match its checklist")
	}
	if len(evidence.Artifacts) == 0 {
		return errors.New("external evidence requires an artifact inventory")
	}
	seenArtifacts := make(map[string]struct{}, len(evidence.Artifacts))
	for _, artifact := range evidence.Artifacts {
		clean := filepath.Clean(artifact.Path)
		if clean == "." || filepath.IsAbs(clean) || clean != artifact.Path || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || !s6SHA256Pattern.MatchString(artifact.SHA256) {
			return errors.New("external evidence artifact path or digest is invalid")
		}
		if _, duplicate := seenArtifacts[clean]; duplicate {
			return errors.New("external evidence artifact is duplicated")
		}
		seenArtifacts[clean] = struct{}{}
		digest, err := sha256File(filepath.Join(artifactRoot, clean))
		if err != nil || digest != artifact.SHA256 {
			return errors.New("external evidence artifact digest does not match")
		}
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("external evidence contains trailing JSON")
	}
	return nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
