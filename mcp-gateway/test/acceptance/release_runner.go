package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
)

type ReleaseCleanupResult struct {
	Processes      bool
	Listeners      bool
	TemporaryRoots bool
	Err            error
}

func runReleaseProfile(ctx context.Context, root string, executor Executor, definition releaseProfileDefinition, external []releaseExternalEvidenceReference, finalize func() ReleaseCleanupResult) (releaseReport, error) {
	if executor == nil || finalize == nil {
		return releaseReport{}, errors.New("release executor and cleanup finalizer are required")
	}
	if err := validateFinalOrFixtureReleaseProfile(definition); err != nil {
		return releaseReport{}, err
	}
	revision, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return releaseReport{}, errors.New("resolve release candidate HEAD")
	}
	status, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil || status != "" {
		return releaseReport{}, errors.New("release acceptance requires a clean workspace")
	}
	profileHash, definitionHash, manifestHash, err := releaseDefinitionHashes(root, definition)
	if err != nil {
		return releaseReport{}, err
	}
	binding := releaseExternalEvidenceBinding{CandidateHead: revision, ProfileHash: profileHash, ManifestHash: manifestHash}
	if err := validateReleaseExternalEvidenceReferences(root, binding, definition.ExternalEvidence, external); err != nil {
		return releaseReport{}, err
	}

	started := time.Now().UTC()
	report := releaseReport{
		SchemaVersion: releaseReportSchemaVersion, Profile: releaseProfile, ProfileHash: profileHash, CommandDefinitionHash: definitionHash, ManifestHash: manifestHash,
		Result: ResultPassed, Reason: "all_checks_passed", Revision: revision, CleanBefore: true, StartedAt: started.Format(time.RFC3339Nano),
		Coverage: cloneReleaseCoverage(definition.Coverage), Checks: []releaseCheck{}, ExternalEvidence: append([]releaseExternalEvidenceReference(nil), external...),
	}
	profileContext, cancelProfile := context.WithTimeout(ctx, time.Duration(definition.BudgetMillis)*time.Millisecond)
	defer cancelProfile()
	for _, expected := range definition.Checks {
		checkStarted := time.Now().UTC()
		checkContext, cancelCheck := context.WithTimeout(profileContext, time.Duration(expected.TimeoutMillis)*time.Millisecond)
		command := Command{CheckName: expected.ID, Name: expected.Argv[0], Arguments: append([]string(nil), expected.Argv[1:]...), Artifacts: append([]string(nil), expected.Artifacts...), Native: expected.Native, Timeout: time.Duration(expected.TimeoutMillis) * time.Millisecond}
		output, commandErr := executor.Run(checkContext, root, command)
		cancelCheck()
		if commandErr == nil && expected.Native {
			native, parseErr := keyringnative.Parse(output)
			if parseErr != nil {
				commandErr = fmt.Errorf("parse native evidence: %w", parseErr)
			} else {
				report.Native = &native
				if native.Result == keyringnative.ResultFailed {
					commandErr = errors.New("native evidence failed")
				}
			}
		}
		checkEnded := time.Now().UTC()
		termination, cleanup, diagnostics := executionMetadata(commandErr)
		check := releaseCheck{
			ID: expected.ID, Status: ResultPassed, Argv: append([]string(nil), expected.Argv...), Coverage: cloneReleaseCoverage(expected.Coverage), Artifacts: append([]string(nil), expected.Artifacts...),
			StartedAt: checkStarted.Format(time.RFC3339Nano), EndedAt: checkEnded.Format(time.RFC3339Nano), DurationMillis: checkEnded.Sub(checkStarted).Milliseconds(), TimeoutMillis: expected.TimeoutMillis,
			Termination: termination, Cleanup: cleanup, CleanupRequirements: append([]string(nil), expected.CleanupRequirements...), DiagnosticPaths: diagnostics,
		}
		if commandErr != nil {
			check.Status = ResultFailed
			check.TimedOut = errors.Is(commandErr, context.DeadlineExceeded)
			report.Result = ResultFailed
			report.Reason = "check_failed:" + expected.ID
		}
		report.Checks = append(report.Checks, check)
		if commandErr != nil {
			break
		}
	}

	cleanup := finalize()
	report.Cleanup = releaseCleanup{Status: "passed", Processes: cleanup.Processes, Listeners: cleanup.Listeners, TemporaryRoots: cleanup.TemporaryRoots}
	if cleanup.Err != nil || !cleanup.Processes || !cleanup.Listeners || !cleanup.TemporaryRoots {
		report.Cleanup.Status = "failed"
		report.Result = ResultFailed
		report.Reason = "release_cleanup_failed"
	}
	finalRevision, revisionErr := gitOutput(ctx, root, "rev-parse", "HEAD")
	finalStatus, statusErr := gitOutput(ctx, root, "status", "--porcelain")
	report.CleanAfter = statusErr == nil && finalStatus == ""
	report.Dirty = !report.CleanAfter
	if revisionErr != nil || finalRevision != revision || !report.CleanAfter {
		report.Result = ResultFailed
		report.Reason = "candidate_state_changed"
	}
	if err := validateReleaseExternalEvidenceReferences(root, binding, definition.ExternalEvidence, report.ExternalEvidence); err != nil {
		report.Result = ResultFailed
		report.Reason = "external_evidence_changed"
	}
	ended := time.Now().UTC()
	report.EndedAt = ended.Format(time.RFC3339Nano)
	report.DurationMillis = ended.Sub(started).Milliseconds()
	if err := validateReleaseReport(report, definition); err != nil {
		return releaseReport{}, err
	}
	return report, nil
}

func RunFinalAcceptance(ctx context.Context, root, reportPath string, executor Executor, finalize func() ReleaseCleanupResult) (bool, error) {
	definition, err := finalReleaseProfile(root)
	if err != nil {
		return false, err
	}
	profileHash, _, manifestHash, err := releaseDefinitionHashes(root, definition)
	if err != nil {
		return false, err
	}
	revision, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	external, err := loadReleaseExternalEvidence(root, releaseExternalEvidenceBinding{CandidateHead: revision, ProfileHash: profileHash, ManifestHash: manifestHash}, definition.ExternalEvidence)
	if err != nil {
		return false, err
	}
	report, err := runReleaseProfile(ctx, root, executor, definition, external, finalize)
	if err != nil {
		return false, err
	}
	if err := writeReleaseReport(root, reportPath, report, definition); err != nil {
		return false, err
	}
	finalRevision, revisionErr := gitOutput(ctx, root, "rev-parse", "HEAD")
	finalStatus, statusErr := gitOutput(ctx, root, "status", "--porcelain")
	if revisionErr != nil || statusErr != nil || finalRevision != revision || finalStatus != "" {
		return false, errors.New("release report publication changed candidate state")
	}
	return report.Result == ResultPassed, nil
}

func AdoptFinalAcceptanceReport(root, reportPath, outputPath string, now func() time.Time) error {
	definition, err := finalReleaseProfile(root)
	if err != nil {
		return err
	}
	profileHash, _, manifestHash, err := releaseDefinitionHashes(root, definition)
	if err != nil {
		return err
	}
	revision, err := gitOutput(context.Background(), root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	binding := releaseExternalEvidenceBinding{CandidateHead: revision, ProfileHash: profileHash, ManifestHash: manifestHash}
	_, err = adoptReleaseReport(root, reportPath, outputPath, definition, func(references []releaseExternalEvidenceReference) error {
		return validateReleaseExternalEvidenceReferences(root, binding, definition.ExternalEvidence, references)
	}, now)
	if err != nil {
		return err
	}
	status, statusErr := gitOutput(context.Background(), root, "status", "--porcelain")
	if statusErr != nil || status != "" {
		return errors.New("release adoption publication changed candidate state")
	}
	return nil
}

func PrepareAndQualifyFinalExternalEvidence(ctx context.Context, root string) error {
	definition, err := finalReleaseProfile(root)
	if err != nil {
		return err
	}
	return prepareAndQualifyReleaseExternalEvidence(ctx, root, definition, defaultReleaseEnvironmentProber)
}

func prepareAndQualifyReleaseExternalEvidence(ctx context.Context, root string, definition releaseProfileDefinition, prober releaseEnvironmentProber) error {
	revision, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	status, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil || status != "" {
		return errors.New("external evidence preparation requires a clean workspace")
	}
	profileHash, _, manifestHash, err := releaseDefinitionHashes(root, definition)
	if err != nil {
		return err
	}
	binding := releaseExternalEvidenceBinding{CandidateHead: revision, ProfileHash: profileHash, ManifestHash: manifestHash}
	for _, external := range definition.ExternalEvidence {
		path := filepath.Join(root, filepath.FromSlash(releaseExternalEvidencePath(external.BehaviorID)))
		if _, statErr := os.Lstat(path); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		templates, generateErr := generateReleaseExternalEvidenceTemplates(binding, []releaseExternalEvidenceDefinition{external}, prober)
		if generateErr != nil {
			return generateErr
		}
		if err := writeAtomic(path, templates[0].Contents); err != nil {
			return err
		}
	}
	_, err = qualifyReleaseExternalEvidence(ctx, root, definition)
	return err
}

func validateFinalOrFixtureReleaseProfile(definition releaseProfileDefinition) error {
	if definition.Profile == releaseProfile && len(definition.Coverage.ProductBehaviors) == len(canonicalReleaseProductBehaviors()) {
		return validateFinalReleaseProfile(definition)
	}
	return validateReleaseProfileDefinition(definition)
}

func writeAtomic(path string, contents []byte) error {
	return writeAtomicValidated(path, contents, func() error { return nil })
}
