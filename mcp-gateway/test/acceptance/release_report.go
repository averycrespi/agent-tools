package acceptance

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	releaseReportSchemaVersion = 4
	releaseProfile             = "accept"
)

var errLegacyAcceptanceReport = errors.New("legacy acceptance report is not compatible with the release profile")

//go:embed release_report.schema.json
var releaseReportSchema []byte

type releaseCoverage struct {
	ProductBehaviors []string `json:"product_behaviors"`
	CleanupCriteria  []string `json:"cleanup_criteria"`
}

type releaseCheckDefinition struct {
	ID                  string          `json:"id"`
	Argv                []string        `json:"argv"`
	Coverage            releaseCoverage `json:"coverage"`
	TimeoutMillis       int64           `json:"timeout_ms"`
	Artifacts           []string        `json:"artifacts"`
	CleanupRequirements []string        `json:"cleanup_requirements"`
	Native              bool            `json:"native,omitempty"`
}

type releaseProfileDefinition struct {
	Profile         string                   `json:"profile"`
	Coverage        releaseCoverage          `json:"coverage"`
	Checks          []releaseCheckDefinition `json:"checks"`
	DefinitionFiles []string                 `json:"-"`
}

type releaseCheck struct {
	ID                  string          `json:"id"`
	Status              string          `json:"status"`
	Argv                []string        `json:"argv"`
	Coverage            releaseCoverage `json:"coverage"`
	Artifacts           []string        `json:"artifacts"`
	StartedAt           string          `json:"started_at"`
	EndedAt             string          `json:"ended_at"`
	DurationMillis      int64           `json:"duration_ms"`
	TimeoutMillis       int64           `json:"timeout_ms"`
	TimedOut            bool            `json:"timed_out"`
	Termination         string          `json:"termination"`
	Cleanup             string          `json:"cleanup"`
	CleanupRequirements []string        `json:"cleanup_requirements"`
	DiagnosticPaths     []string        `json:"diagnostic_paths"`
}

type releaseExternalEvidenceReference struct {
	ID       string `json:"id"`
	Result   string `json:"result"`
	Blocking bool   `json:"blocking"`
	Path     string `json:"path,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}

type releaseCleanup struct {
	Status         string `json:"status"`
	Processes      bool   `json:"processes"`
	Listeners      bool   `json:"listeners"`
	TemporaryRoots bool   `json:"temporary_roots"`
}

type releaseReport struct {
	SchemaVersion         int                                `json:"schema_version"`
	Profile               string                             `json:"profile"`
	ProfileHash           string                             `json:"profile_hash"`
	CommandDefinitionHash string                             `json:"command_definition_hash"`
	ManifestHash          string                             `json:"manifest_hash"`
	Result                string                             `json:"result"`
	Reason                string                             `json:"reason"`
	Revision              string                             `json:"revision"`
	Dirty                 bool                               `json:"dirty"`
	CleanBefore           bool                               `json:"clean_before"`
	CleanAfter            bool                               `json:"clean_after"`
	StartedAt             string                             `json:"started_at"`
	EndedAt               string                             `json:"ended_at"`
	DurationMillis        int64                              `json:"duration_ms"`
	Coverage              releaseCoverage                    `json:"coverage"`
	Checks                []releaseCheck                     `json:"checks"`
	Native                *keyringnative.Result              `json:"native,omitempty"`
	ExternalEvidence      []releaseExternalEvidenceReference `json:"external_evidence"`
	Cleanup               releaseCleanup                     `json:"cleanup"`
}

type releaseAdoption struct {
	SchemaVersion         int                                `json:"schema_version"`
	ReportSHA256          string                             `json:"report_sha256"`
	Revision              string                             `json:"revision"`
	Profile               string                             `json:"profile"`
	ProfileHash           string                             `json:"profile_hash"`
	CommandDefinitionHash string                             `json:"command_definition_hash"`
	ManifestHash          string                             `json:"manifest_hash"`
	Coverage              releaseCoverage                    `json:"coverage"`
	NativeResult          string                             `json:"native_result"`
	ExternalEvidence      []releaseExternalEvidenceReference `json:"external_evidence"`
	CleanAfter            bool                               `json:"clean_after"`
	AdoptedAt             string                             `json:"adopted_at"`
}

type releaseDefinitionFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type releaseCommandDefinitions struct {
	ProfileHash string                  `json:"profile_hash"`
	Files       []releaseDefinitionFile `json:"files"`
}

func parseReleaseReport(contents []byte, definition releaseProfileDefinition) (releaseReport, error) {
	var discriminator struct {
		SchemaVersion int    `json:"schema_version"`
		Profile       string `json:"profile"`
	}
	if err := json.Unmarshal(contents, &discriminator); err == nil {
		legacyProfile := map[string]bool{"s2_1": true, "s3": true, "s4": true, "s5": true, "s6": true}[discriminator.Profile]
		if discriminator.SchemaVersion > 0 && discriminator.SchemaVersion < releaseReportSchemaVersion || legacyProfile {
			return releaseReport{}, errLegacyAcceptanceReport
		}
	}
	var report releaseReport
	if err := strictjson.Decode(contents, &report, strictjson.Options{MaxBytes: 128 * 1024, MaxDepth: 12, RejectUnknownMembers: true}); err != nil {
		return releaseReport{}, err
	}
	if err := validateReleaseReportSchema(contents); err != nil {
		return releaseReport{}, err
	}
	if err := validateReleaseReport(report, definition); err != nil {
		return releaseReport{}, err
	}
	return report, nil
}

func validateReleaseReportSchema(contents []byte) error {
	var schemaDocument any
	if err := json.Unmarshal(releaseReportSchema, &schemaDocument); err != nil {
		return fmt.Errorf("decode release report schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:mcp-gateway:release-report", schemaDocument); err != nil {
		return fmt.Errorf("load release report schema: %w", err)
	}
	schema, err := compiler.Compile("urn:mcp-gateway:release-report")
	if err != nil {
		return fmt.Errorf("compile release report schema: %w", err)
	}
	var instance any
	if err := json.Unmarshal(contents, &instance); err != nil {
		return err
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("validate release report schema: %w", err)
	}
	return nil
}

func validateReleaseReport(report releaseReport, definition releaseProfileDefinition) error {
	if err := validateReleaseProfileDefinition(definition); err != nil {
		return err
	}
	profileHash, manifestHash, err := releaseProfileAndManifestHashes(definition)
	if err != nil {
		return err
	}
	if report.SchemaVersion != releaseReportSchemaVersion || report.Profile != releaseProfile || report.Profile != definition.Profile {
		return errors.New("release report does not match the closed profile")
	}
	if report.ProfileHash != profileHash {
		return errors.New("release profile hash does not match the closed profile")
	}
	if report.ManifestHash != manifestHash || !reflect.DeepEqual(report.Coverage, definition.Coverage) {
		return errors.New("release manifest does not match the closed coverage")
	}
	if !digestPattern.MatchString(report.CommandDefinitionHash) {
		return errors.New("release command definition hash is invalid")
	}
	if err := validateTiming(report.StartedAt, report.EndedAt, report.DurationMillis); err != nil {
		return fmt.Errorf("invalid release report timing: %w", err)
	}
	reportStart, _ := time.Parse(time.RFC3339Nano, report.StartedAt)
	reportEnd, _ := time.Parse(time.RFC3339Nano, report.EndedAt)
	if len(report.Checks) > len(definition.Checks) {
		return errors.New("release report has more checks than its profile")
	}
	for index, check := range report.Checks {
		expected := definition.Checks[index]
		if check.ID != expected.ID || !reflect.DeepEqual(check.Argv, expected.Argv) || !reflect.DeepEqual(check.Coverage, expected.Coverage) || !reflect.DeepEqual(check.Artifacts, expected.Artifacts) || !reflect.DeepEqual(check.CleanupRequirements, expected.CleanupRequirements) || check.TimeoutMillis != expected.TimeoutMillis {
			return errors.New("release check does not match the closed profile")
		}
		if err := validateTiming(check.StartedAt, check.EndedAt, check.DurationMillis); err != nil {
			return fmt.Errorf("invalid release check timing: %w", err)
		}
		checkStart, _ := time.Parse(time.RFC3339Nano, check.StartedAt)
		checkEnd, _ := time.Parse(time.RFC3339Nano, check.EndedAt)
		if checkStart.Before(reportStart) || checkEnd.After(reportEnd) {
			return errors.New("release check timing is outside report timing")
		}
		if index > 0 {
			previousEnd, _ := time.Parse(time.RFC3339Nano, report.Checks[index-1].EndedAt)
			if checkStart.Before(previousEnd) {
				return errors.New("release checks overlap or are out of order")
			}
		}
		if check.TimedOut && check.Status != ResultFailed || check.Cleanup == "failed" && check.Status != ResultFailed {
			return errors.New("timeout or cleanup failure must fail its release check")
		}
		if check.Status == ResultPassed && check.Termination != "none" {
			return errors.New("passed release check cannot require termination")
		}
		for _, path := range append(append([]string(nil), check.Artifacts...), check.DiagnosticPaths...) {
			if err := validateReleaseRelativePath(path); err != nil {
				return err
			}
		}
	}
	if report.Native != nil {
		encoded, err := json.Marshal(report.Native)
		if err != nil {
			return err
		}
		if _, err := keyringnative.Parse(encoded); err != nil {
			return fmt.Errorf("invalid release native evidence: %w", err)
		}
	}
	for _, external := range report.ExternalEvidence {
		if err := validateReleaseExternalReference(external); err != nil {
			return err
		}
	}
	if report.Result == ResultPassed {
		if report.Reason != "all_checks_passed" || len(report.Checks) != len(definition.Checks) || report.Dirty || !report.CleanBefore || !report.CleanAfter {
			return errors.New("passed release report requires every check and a clean unchanged workspace")
		}
		for _, check := range report.Checks {
			if check.Status != ResultPassed {
				return errors.New("passed release report cannot contain a failed check")
			}
		}
		if report.Native == nil || report.Native.Result == keyringnative.ResultFailed {
			return errors.New("passed release report requires nonfailed native evidence")
		}
		for _, external := range report.ExternalEvidence {
			if external.Result == "failed" || external.Result == "unavailable" && external.Blocking {
				return errors.New("passed release report cannot contain failed blocking external evidence")
			}
		}
		if report.Cleanup.Status != "passed" || !report.Cleanup.Processes || !report.Cleanup.Listeners || !report.Cleanup.TemporaryRoots {
			return errors.New("passed release report requires complete cleanup")
		}
	} else {
		if report.Result != ResultFailed || report.Reason == "" || report.Reason == "all_checks_passed" {
			return errors.New("failed release report requires a failure reason")
		}
		if len(report.Checks) > 0 && report.Checks[len(report.Checks)-1].Status != ResultFailed {
			return errors.New("failed release report must end at its failed check")
		}
	}
	return nil
}

func validateReleaseProfileDefinition(definition releaseProfileDefinition) error {
	if definition.Profile != releaseProfile || len(definition.Checks) == 0 || len(definition.DefinitionFiles) == 0 {
		return errors.New("release profile definition is incomplete")
	}
	if err := validateReleaseCoverage(definition.Coverage); err != nil {
		return err
	}
	seenChecks := make(map[string]struct{}, len(definition.Checks))
	productOwners := make(map[string]int)
	cleanupOwners := make(map[string]int)
	for _, check := range definition.Checks {
		if check.ID == "" || len(check.Argv) == 0 || check.TimeoutMillis <= 0 || len(check.Artifacts) == 0 || len(check.CleanupRequirements) == 0 {
			return fmt.Errorf("release check definition %s is incomplete", check.ID)
		}
		if _, duplicate := seenChecks[check.ID]; duplicate {
			return fmt.Errorf("duplicate release check %s", check.ID)
		}
		seenChecks[check.ID] = struct{}{}
		if err := validateReleaseCoverage(check.Coverage); err != nil {
			return err
		}
		for _, id := range check.Coverage.ProductBehaviors {
			productOwners[id]++
		}
		for _, id := range check.Coverage.CleanupCriteria {
			cleanupOwners[id]++
		}
		for _, path := range check.Artifacts {
			if err := validateReleaseRelativePath(path); err != nil {
				return err
			}
		}
	}
	for _, id := range definition.Coverage.ProductBehaviors {
		if productOwners[id] == 0 {
			return fmt.Errorf("release product behavior %s has no check owner", id)
		}
	}
	for _, id := range definition.Coverage.CleanupCriteria {
		if cleanupOwners[id] == 0 {
			return fmt.Errorf("release cleanup criterion %s has no check owner", id)
		}
	}
	return nil
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validateReleaseCoverage(coverage releaseCoverage) error {
	known := releaseBehaviorIDs()
	seen := make(map[string]struct{})
	for _, id := range coverage.ProductBehaviors {
		if _, ok := known[id]; !ok || strings.HasPrefix(id, "cleanup.") {
			return fmt.Errorf("unknown release product behavior %s", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate release product behavior %s", id)
		}
		seen[id] = struct{}{}
	}
	seen = make(map[string]struct{})
	for _, id := range coverage.CleanupCriteria {
		valid := false
		for index := 1; index <= 10; index++ {
			if id == fmt.Sprintf("cleanup.AC-%d", index) {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("unknown release cleanup criterion %s", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate release cleanup criterion %s", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func releaseBehaviorIDs() map[string]struct{} {
	ids := make(map[string]struct{})
	for _, row := range contract.ProductBehaviorManifest() {
		ids[row.ID] = struct{}{}
	}
	for _, row := range contract.SecurityBehaviorManifest() {
		ids[row.ID] = struct{}{}
	}
	for _, row := range contract.PredecessorBehaviorManifest() {
		ids[row.ID] = struct{}{}
	}
	for _, row := range contract.DocumentationBehaviorManifest() {
		ids[row.ID] = struct{}{}
	}
	for _, row := range contract.EvidenceTierManifest() {
		ids[row.ID] = struct{}{}
	}
	return ids
}

func validateReleaseExternalReference(reference releaseExternalEvidenceReference) error {
	switch reference.Result {
	case "passed":
		if reference.Path == "" || !digestPattern.MatchString(reference.SHA256) {
			return errors.New("passed external evidence requires a path and digest")
		}
		return validateReleaseRelativePath(reference.Path)
	case "unavailable":
		if reference.Blocking || reference.Path != "" || reference.SHA256 != "" {
			return errors.New("only additive external evidence may be unavailable")
		}
	case "failed":
		if reference.Path != "" {
			return validateReleaseRelativePath(reference.Path)
		}
	default:
		return errors.New("unknown release external evidence result")
	}
	return nil
}

func validateReleaseRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == "." || strings.HasPrefix(path, "../") {
		return fmt.Errorf("release artifact path %q is not a clean repository-relative path", path)
	}
	return nil
}

func releaseProfileAndManifestHashes(definition releaseProfileDefinition) (string, string, error) {
	profile := definition
	profile.DefinitionFiles = nil
	encoded, err := json.Marshal(profile)
	if err != nil {
		return "", "", fmt.Errorf("encode release profile: %w", err)
	}
	profileHash := releaseDigest(encoded)
	manifest, err := json.Marshal(definition.Coverage)
	if err != nil {
		return "", "", fmt.Errorf("encode release manifest: %w", err)
	}
	return profileHash, releaseDigest(manifest), nil
}

func releaseDefinitionHashes(root string, definition releaseProfileDefinition) (string, string, string, error) {
	if err := validateReleaseProfileDefinition(definition); err != nil {
		return "", "", "", err
	}
	profileHash, manifestHash, err := releaseProfileAndManifestHashes(definition)
	if err != nil {
		return "", "", "", err
	}
	paths := append([]string(nil), definition.DefinitionFiles...)
	sort.Strings(paths)
	files := make([]releaseDefinitionFile, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if err := validateReleaseRelativePath(path); err != nil {
			return "", "", "", err
		}
		if _, duplicate := seen[path]; duplicate {
			return "", "", "", fmt.Errorf("duplicate release definition path %s", path)
		}
		seen[path] = struct{}{}
		absolute := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(absolute)
		if err != nil {
			return "", "", "", fmt.Errorf("read release definition %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", "", "", fmt.Errorf("release definition %s is not a regular file", path)
		}
		contents, err := os.ReadFile(absolute) //nolint:gosec // The closed definition inventory is validated as clean relative paths below root.
		if err != nil {
			return "", "", "", err
		}
		files = append(files, releaseDefinitionFile{Path: path, SHA256: releaseDigest(contents)})
	}
	encoded, err := json.Marshal(releaseCommandDefinitions{ProfileHash: profileHash, Files: files})
	if err != nil {
		return "", "", "", err
	}
	return profileHash, releaseDigest(encoded), manifestHash, nil
}

func validateReleaseReportDefinitions(root string, report releaseReport, definition releaseProfileDefinition) error {
	profileHash, definitionHash, manifestHash, err := releaseDefinitionHashes(root, definition)
	if err != nil {
		return err
	}
	if report.ProfileHash != profileHash || report.ManifestHash != manifestHash {
		return errors.New("release profile or manifest hash does not match current definitions")
	}
	if report.CommandDefinitionHash != definitionHash {
		return errors.New("release command definition hash does not match current definitions")
	}
	return nil
}

func writeReleaseReport(root, path string, report releaseReport, definition releaseProfileDefinition) error {
	contents, err := json.Marshal(report)
	if err != nil {
		return err
	}
	parsed, err := parseReleaseReport(contents, definition)
	if err != nil {
		return err
	}
	if err := validateReleaseReportDefinitions(root, parsed, definition); err != nil {
		return err
	}
	return writeAtomic(path, contents)
}

func adoptReleaseReport(root, reportPath, outputPath string, definition releaseProfileDefinition, validateExternal func(releaseExternalEvidenceReference) error, now func() time.Time) (releaseAdoption, error) {
	if err := distinctReleasePaths(reportPath, outputPath); err != nil {
		return releaseAdoption{}, err
	}
	contents, err := os.ReadFile(reportPath) //nolint:gosec // The caller selects the immutable report artifact.
	if err != nil {
		return releaseAdoption{}, err
	}
	report, err := parseReleaseReport(contents, definition)
	if err != nil {
		return releaseAdoption{}, err
	}
	if report.Result != ResultPassed || report.Dirty || !report.CleanBefore || !report.CleanAfter {
		return releaseAdoption{}, errors.New("only a passed clean release report can be adopted")
	}
	digest := sha256.Sum256(contents)
	verify := func() error {
		current, readErr := os.ReadFile(reportPath) //nolint:gosec // Revalidate the caller-selected immutable report before publication.
		if readErr != nil || sha256.Sum256(current) != digest {
			return errors.New("release report changed during adoption")
		}
		if err := validateReleaseReportDefinitions(root, report, definition); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		revision, revisionErr := gitOutput(ctx, root, "rev-parse", "HEAD")
		status, statusErr := gitOutput(ctx, root, "status", "--porcelain")
		if revisionErr != nil || revision != report.Revision {
			return errors.New("release report revision does not match current HEAD")
		}
		if statusErr != nil || status != "" {
			return errors.New("release report adoption requires a clean workspace")
		}
		if validateExternal != nil {
			for _, reference := range report.ExternalEvidence {
				if err := validateExternal(reference); err != nil {
					return fmt.Errorf("release external evidence changed: %w", err)
				}
			}
		}
		return nil
	}
	if err := verify(); err != nil {
		return releaseAdoption{}, err
	}
	nativeResult := ""
	if report.Native != nil {
		nativeResult = report.Native.Result
	}
	adoption := releaseAdoption{
		SchemaVersion: 1, ReportSHA256: "sha256:" + hex.EncodeToString(digest[:]), Revision: report.Revision,
		Profile: report.Profile, ProfileHash: report.ProfileHash, CommandDefinitionHash: report.CommandDefinitionHash,
		ManifestHash: report.ManifestHash, Coverage: cloneReleaseCoverage(report.Coverage), NativeResult: nativeResult,
		ExternalEvidence: append([]releaseExternalEvidenceReference(nil), report.ExternalEvidence...), CleanAfter: true,
		AdoptedAt: now().UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(adoption)
	if err != nil {
		return releaseAdoption{}, err
	}
	if err := writeAtomicValidated(outputPath, encoded, verify); err != nil {
		return releaseAdoption{}, err
	}
	return adoption, nil
}

func distinctReleasePaths(reportPath, outputPath string) error {
	reportAbsolute, err := filepath.Abs(reportPath)
	if err != nil {
		return err
	}
	outputAbsolute, err := filepath.Abs(outputPath)
	if err != nil {
		return err
	}
	if reportAbsolute == outputAbsolute {
		return errors.New("release report and adoption output must be distinct")
	}
	reportInfo, reportErr := os.Stat(reportAbsolute)
	outputInfo, outputErr := os.Stat(outputAbsolute)
	if reportErr == nil && outputErr == nil && os.SameFile(reportInfo, outputInfo) {
		return errors.New("release report and adoption output must be distinct")
	}
	return nil
}

func writeAtomicValidated(path string, contents []byte, validate func() error) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".release-*") //nolint:gosec // The caller owns the selected output directory.
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(contents, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := validate(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func cloneReleaseCoverage(coverage releaseCoverage) releaseCoverage {
	return releaseCoverage{ProductBehaviors: append([]string(nil), coverage.ProductBehaviors...), CleanupCriteria: append([]string(nil), coverage.CleanupCriteria...)}
}

func releaseDigest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(sum[:])
}
