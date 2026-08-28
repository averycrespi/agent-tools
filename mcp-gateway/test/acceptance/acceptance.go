package acceptance

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	ResultPassed = "passed"
	ResultFailed = "failed"

	ProfileS21 Profile = "s2_1"
	ProfileS3  Profile = "s3"
	ProfileS4  Profile = "s4"
	ProfileS5  Profile = "s5"

	s5ProfileTimeout = 15 * time.Minute
	s5CleanupReserve = 15 * time.Second
)

type Profile string

type Check struct {
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	Command         string   `json:"command"`
	Criteria        []string `json:"criteria"`
	Evidence        []string `json:"evidence,omitempty"`
	Artifacts       []string `json:"artifacts"`
	StartedAt       string   `json:"started_at"`
	EndedAt         string   `json:"ended_at"`
	DurationMillis  int64    `json:"duration_ms"`
	TimeoutMillis   int64    `json:"timeout_ms"`
	TimedOut        bool     `json:"timed_out"`
	Termination     string   `json:"termination"`
	Cleanup         string   `json:"cleanup"`
	DiagnosticPaths []string `json:"diagnostic_paths"`
}

type Report struct {
	SchemaVersion         int                           `json:"schema_version"`
	Profile               Profile                       `json:"profile"`
	ProfileHash           string                        `json:"profile_hash"`
	CommandDefinitionHash string                        `json:"command_definition_hash"`
	Result                string                        `json:"result"`
	Revision              string                        `json:"revision"`
	Dirty                 bool                          `json:"dirty"`
	DirtyPolicy           string                        `json:"dirty_policy"`
	CleanBefore           bool                          `json:"clean_before"`
	CleanAfter            bool                          `json:"clean_after"`
	StartedAt             string                        `json:"started_at"`
	EndedAt               string                        `json:"ended_at"`
	DurationMillis        int64                         `json:"duration_ms"`
	Reason                string                        `json:"reason"`
	Checks                []Check                       `json:"checks"`
	EvidenceManifest      []contract.AcceptanceEvidence `json:"evidence_manifest,omitempty"`
	ClauseManifest        []contract.ClauseEvidence     `json:"clause_manifest,omitempty"`
	Native                *keyringnative.Result         `json:"native,omitempty"`
}

type Command struct {
	CheckName string
	Name      string
	Arguments []string
	Criteria  []string
	Evidence  []string
	Artifacts []string
	Native    bool          `json:"native,omitempty"`
	Timeout   time.Duration `json:"timeout,omitempty"`
}

type Executor interface {
	Run(context.Context, string, Command) ([]byte, error)
}

type OSExecutor struct{}

type commandExecutionError struct {
	cause           error
	termination     string
	cleanup         string
	diagnosticPaths []string
}

func (failure *commandExecutionError) Error() string { return failure.cause.Error() }
func (failure *commandExecutionError) Unwrap() error { return failure.cause }

func (OSExecutor) Run(ctx context.Context, root string, command Command) ([]byte, error) {
	runner, err := testutil.NewBinaryRunner(19*time.Minute, 4*1024*1024)
	if err != nil {
		return nil, err
	}
	result, runErr := runner.RunInDir(ctx, root, command.Name, command.Arguments...)
	_, _ = os.Stderr.Write(result.Stdout)
	_, _ = os.Stderr.Write(result.Stderr)
	cleanupErr := testutil.CleanupInheritedProcesses()
	if result.StdoutTruncated || result.StderrTruncated {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("acceptance command output exceeded its bound"))
	}
	combinedErr := errors.Join(runErr, cleanupErr)
	if combinedErr != nil {
		termination := "none"
		if result.Cleanup.KillSent {
			termination = "kill"
		} else if result.Cleanup.TermSent {
			termination = "term"
		}
		cleanup := "passed"
		if cleanupErr != nil || result.Cleanup.Survived {
			cleanup = "failed"
		}
		combinedErr = &commandExecutionError{cause: combinedErr, termination: termination, cleanup: cleanup}
	}
	if command.Native {
		return result.Stdout, combinedErr
	}
	return nil, combinedErr
}

//go:embed report.schema.json
var reportSchema []byte

func Commands() []Command {
	commands, _ := ProfileCommands(ProfileS21)
	return commands
}

func ProfileCommands(profile Profile) ([]Command, error) {
	switch profile {
	case ProfileS21:
		return []Command{
			{CheckName: "focused_race_guards", Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "./internal/composition", "./internal/downstream", "./internal/oauth", "./internal/contract", "./internal/backup", "./internal/keyring", "./test/material", "./test/keyringnative"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-6"}, Artifacts: []string{"mcp-gateway/internal/composition/source_test.go", "mcp-gateway/internal/downstream/source_test.go", "mcp-gateway/internal/oauth/source_test.go", "mcp-gateway/internal/contract/documentation_test.go"}},
			{CheckName: "test_integration", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-integration"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-6"}, Artifacts: []string{"mcp-gateway/internal/composition", "mcp-gateway/internal/servers"}},
			{CheckName: "test_e2e", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-e2e"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6"}, Artifacts: []string{"mcp-gateway/test/e2e"}},
			{CheckName: "audit", Name: "make", Arguments: []string{"-C", "mcp-gateway", "audit"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}},
			{CheckName: "unsuppressed_govulncheck", Name: "go", Arguments: []string{"-C", "mcp-gateway", "tool", "govulncheck", "./..."}, Criteria: []string{"AC-6"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}},
			{CheckName: "native_keyring", Name: "mcp-gateway/test/keyring-native.sh", Criteria: []string{"AC-1", "AC-6"}, Artifacts: []string{"mcp-gateway/test/keyringnative/result.schema.json", "mcp-gateway/test/material/material_test.go"}, Native: true},
			{CheckName: "repository_check", Name: "make", Arguments: []string{"check"}, Criteria: []string{"AC-6"}, Artifacts: []string{"Makefile", "package.json"}},
			{CheckName: "diff_check", Name: "git", Arguments: []string{"diff", "--check"}, Criteria: []string{"AC-6"}, Artifacts: []string{"mcp-gateway/README.md", "mcp-gateway/DESIGN.md", "mcp-gateway/CLAUDE.md"}},
		}, nil
	case ProfileS3:
		return []Command{
			{CheckName: "contract_storage", Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "./internal/contract", "./internal/strictjson", "./internal/storage", "./internal/servers"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-6"}, Evidence: []string{"contract", "strictjson-generated", "migration-restore", "docs"}, Artifacts: []string{"mcp-gateway/internal/contract", "mcp-gateway/internal/strictjson", "mcp-gateway/internal/storage/schema8_test.go", "mcp-gateway/internal/contract/documentation_test.go"}},
			{CheckName: "authority_discovery_ingress", Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "-count=10", "./internal/authorization", "./internal/discovery", "./internal/mcpingress"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6"}, Evidence: []string{"authorization-race", "authorization-fuzz", "discovery-race", "ingress-race", "mcp-wire"}, Artifacts: []string{"mcp-gateway/internal/authorization", "mcp-gateway/internal/discovery", "mcp-gateway/internal/mcpingress"}},
			{CheckName: "control_composition_recovery", Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "./internal/api", "./internal/catalog", "./internal/composition", "./internal/backup", "./cmd/mcp-gateway", "./internal/downstream", "./internal/oauth", "./internal/keyring", "./test/material", "./test/keyringnative"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6"}, Evidence: []string{"api-wire", "composition-race", "restore-canary", "source-secret", "source-slice"}, Artifacts: []string{"mcp-gateway/internal/api", "mcp-gateway/internal/composition/source_test.go", "mcp-gateway/internal/downstream/source_test.go", "mcp-gateway/internal/oauth/source_test.go", "mcp-gateway/internal/backup/restore_test.go", "mcp-gateway/cmd/mcp-gateway", "mcp-gateway/test/material"}},
			{CheckName: "integration", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-integration"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-6"}, Evidence: []string{"authorization-integration", "migration-restore", "integration"}, Artifacts: []string{"mcp-gateway/internal/authorization", "mcp-gateway/internal/backup/restore_integration_test.go", "mcp-gateway/internal/storage/fixture_integration.go"}},
			{CheckName: "e2e", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-e2e"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6"}, Evidence: []string{"e2e", "e2e-lifecycle", "e2e-discovery", "restore-canary", "source-secret"}, Artifacts: []string{"mcp-gateway/test/e2e"}},
			{CheckName: "audit", Name: "make", Arguments: []string{"-C", "mcp-gateway", "audit"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6"}, Evidence: []string{"audit"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}},
			{CheckName: "vulnerability", Name: "go", Arguments: []string{"-C", "mcp-gateway", "tool", "govulncheck", "./..."}, Criteria: []string{"AC-6"}, Evidence: []string{"vulnerability"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}},
			{CheckName: "native", Name: "mcp-gateway/test/keyring-native.sh", Criteria: []string{"AC-1", "AC-6"}, Evidence: []string{"native"}, Artifacts: []string{"mcp-gateway/test/keyringnative/result.schema.json", "mcp-gateway/test/material/material_test.go"}, Native: true},
			{CheckName: "repository_check", Name: "make", Arguments: []string{"check"}, Criteria: []string{"AC-6"}, Evidence: []string{"repository-check"}, Artifacts: []string{"Makefile", "package.json"}},
			{CheckName: "diff_check", Name: "git", Arguments: []string{"diff", "--check"}, Criteria: []string{"AC-6"}, Evidence: []string{"docs"}, Artifacts: []string{"mcp-gateway/README.md", "mcp-gateway/DESIGN.md", "mcp-gateway/CLAUDE.md"}},
		}, nil
	case ProfileS4:
		return []Command{
			{CheckName: "contract_privacy_acceptance", Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "./internal/contract", "./internal/invocation", "./internal/storage", "./internal/composition", "./test/material", "./test/acceptance"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, Evidence: []string{"s4-contract", "s4-schema", "s4-redaction-fuzz", "s4-sanitizer-fuzz", "s4-source-guards", "s4-source-privacy", "s4-docs", "s4-acceptance"}, Artifacts: []string{"mcp-gateway/internal/contract/s4_contract_test.go", "mcp-gateway/internal/storage/migrations/009_invocations.sql", "mcp-gateway/internal/invocation/redaction_test.go", "mcp-gateway/internal/invocation/result_test.go", "mcp-gateway/internal/composition/source_test.go", "mcp-gateway/test/material", "mcp-gateway/test/acceptance"}},
			{CheckName: "repeated_admission_races", Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "-count=20", "-timeout=18m", "./internal/invocation", "./internal/authorization"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, Evidence: []string{"s4-admission-race", "s4-invocation-race"}, Artifacts: []string{"mcp-gateway/internal/invocation", "mcp-gateway/internal/authorization"}},
			{CheckName: "repeated_transport_races", Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "-count=20", "-timeout=18m", "./internal/catalog", "./internal/downstream", "./internal/mcpingress"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4"}, Evidence: []string{"s4-catalog-race", "s4-downstream-race", "s4-ingress-wire"}, Artifacts: []string{"mcp-gateway/internal/catalog", "mcp-gateway/internal/downstream", "mcp-gateway/internal/mcpingress"}},
			{CheckName: "repeated_composition_races", Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "-count=20", "-timeout=18m", "./internal/composition"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, Evidence: []string{"s4-composition-race", "s4-lifecycle-race"}, Artifacts: []string{"mcp-gateway/internal/composition"}},
			{CheckName: "integration", Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "-tags=integration", "./internal/storage", "./internal/backup", "./internal/invocation", "./internal/authorization", "./internal/catalog", "./internal/mcpingress", "./internal/composition"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, Evidence: []string{"s4-repository-integration"}, Artifacts: []string{"mcp-gateway/internal/storage", "mcp-gateway/internal/backup", "mcp-gateway/internal/invocation", "mcp-gateway/internal/authorization", "mcp-gateway/internal/catalog", "mcp-gateway/internal/mcpingress", "mcp-gateway/internal/composition"}},
			{CheckName: "e2e", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-e2e"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, Evidence: []string{"s4-e2e-call", "s4-e2e-lifecycle", "s4-e2e-privacy"}, Artifacts: []string{"mcp-gateway/test/e2e"}},
			{CheckName: "audit", Name: "make", Arguments: []string{"-C", "mcp-gateway", "audit"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, Evidence: []string{"s4-audit"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}},
			{CheckName: "vulnerability", Name: "go", Arguments: []string{"-C", "mcp-gateway", "tool", "govulncheck", "./..."}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, Evidence: []string{"s4-vulnerability"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}},
			{CheckName: "native", Name: "mcp-gateway/test/keyring-native.sh", Criteria: []string{"AC-4"}, Artifacts: []string{"mcp-gateway/test/keyringnative/result.schema.json", "mcp-gateway/test/material/material_test.go"}, Native: true},
			{CheckName: "repository_check", Name: "make", Arguments: []string{"check"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, Evidence: []string{"s4-repository-check"}, Artifacts: []string{"Makefile", "package.json"}},
			{CheckName: "diff_check", Name: "git", Arguments: []string{"diff", "--check"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, Artifacts: []string{"README.md", "mcp-gateway/README.md", "mcp-gateway/DESIGN.md", "mcp-gateway/CLAUDE.md"}},
		}, nil
	case ProfileS5:
		allCriteria := []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6", "AC-7"}
		return []Command{
			{CheckName: "format_check", Name: "npm", Arguments: []string{"run", "format:check"}, Criteria: []string{"AC-6", "AC-7"}, Evidence: []string{"s5-docs"}, Artifacts: []string{"package.json"}, Timeout: 2 * time.Second},
			{CheckName: "gateway_verify", Name: "make", Arguments: []string{"-C", "mcp-gateway", "verify"}, Criteria: []string{"AC-6", "AC-7"}, Evidence: []string{"s5-source-guards"}, Artifacts: []string{"mcp-gateway/Makefile"}, Timeout: 10 * time.Second},
			{CheckName: "security", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-security-s5"}, Criteria: []string{"AC-1", "AC-6", "AC-7"}, Evidence: []string{"s5-security"}, Artifacts: []string{"mcp-gateway/test/security"}, Timeout: 30 * time.Second},
			{CheckName: "unit", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-unit"}, Criteria: allCriteria, Evidence: []string{"s5-contract", "s5-unit", "s5-acceptance"}, Artifacts: []string{"mcp-gateway/internal", "mcp-gateway/test/acceptance"}, Timeout: 90 * time.Second},
			{CheckName: "integration", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-integration-s5"}, Criteria: []string{"AC-2", "AC-3", "AC-4", "AC-5", "AC-6", "AC-7"}, Evidence: []string{"s5-integration"}, Artifacts: []string{"mcp-gateway/internal/grantrequests", "mcp-gateway/internal/composition"}, Timeout: 30 * time.Second},
			{CheckName: "stress", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-stress-s5", "STRESS_COUNT=20"}, Criteria: []string{"AC-2", "AC-4", "AC-5", "AC-7"}, Evidence: []string{"s5-stress"}, Artifacts: []string{"mcp-gateway/internal/grantrequests", "mcp-gateway/internal/selfservice", "mcp-gateway/internal/composition"}, Timeout: 4 * time.Minute},
			{CheckName: "e2e", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-e2e"}, Criteria: allCriteria, Evidence: []string{"s5-e2e"}, Artifacts: []string{"mcp-gateway/test/e2e"}, Timeout: 90 * time.Second},
			{CheckName: "vulnerability", Name: "go", Arguments: []string{"-C", "mcp-gateway", "tool", "govulncheck", "./..."}, Criteria: []string{"AC-6", "AC-7"}, Evidence: []string{"s5-vulnerability"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}, Timeout: 5 * time.Second},
			{CheckName: "native", Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-keyring-native"}, Criteria: []string{"AC-6", "AC-7"}, Evidence: []string{"s5-native"}, Artifacts: []string{"mcp-gateway/test/keyringnative/result.schema.json", "mcp-gateway/test/keyring-native.sh"}, Native: true, Timeout: 30 * time.Second},
			{CheckName: "other_tools", Name: "make", Arguments: []string{"check-other-tools"}, Criteria: []string{"AC-6", "AC-7"}, Evidence: []string{"s5-other-tools"}, Artifacts: []string{"Makefile"}, Timeout: 15 * time.Second},
			{CheckName: "diff_check", Name: "git", Arguments: []string{"diff", "--check", "85e81320d7aec44e6c8b4a6bee6f2208d539857f..HEAD", "--"}, Criteria: []string{"AC-6", "AC-7"}, Artifacts: []string{"README.md", "mcp-gateway/README.md", "mcp-gateway/DESIGN.md", "mcp-gateway/CLAUDE.md"}, Timeout: time.Second},
		}, nil
	default:
		return nil, fmt.Errorf("unknown acceptance profile %q", profile)
	}
}

func ProfileDeadline(profile Profile) (time.Duration, time.Duration, bool) {
	if profile != ProfileS5 {
		return 0, 0, false
	}
	return s5ProfileTimeout, s5CleanupReserve, true
}

func Run(ctx context.Context, root string, executor Executor, allowDirty bool) Report {
	return RunProfile(ctx, root, executor, ProfileS21, allowDirty)
}

func RunProfile(ctx context.Context, root string, executor Executor, profile Profile, allowDirty bool) Report {
	started := time.Now().UTC()
	report := Report{
		SchemaVersion: 3, Profile: profile, Result: ResultPassed, DirtyPolicy: "required_clean", Checks: []Check{},
		StartedAt: started.Format(time.RFC3339Nano),
	}
	commands, err := ProfileCommands(profile)
	if err != nil {
		return finishReport(fail(report, "unknown_profile"), started)
	}
	switch profile {
	case ProfileS3:
		report.EvidenceManifest = contract.AcceptanceEvidenceManifest()
	case ProfileS4:
		report.EvidenceManifest = contract.S4AcceptanceEvidenceManifest()
		report.ClauseManifest = contract.S4ClauseEvidenceManifest()
	case ProfileS5:
		report.EvidenceManifest = contract.S5AcceptanceEvidenceManifest()
		report.ClauseManifest = contract.S5ClauseEvidenceManifest()
	}
	if allowDirty {
		report.DirtyPolicy = "allowed"
	}
	revision, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return finishReport(fail(report, "git_revision_unavailable"), started)
	}
	report.Revision = revision
	status, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil {
		return finishReport(fail(report, "git_status_unavailable"), started)
	}
	report.Dirty = status != ""
	report.CleanBefore = !report.Dirty
	report.CleanAfter = report.CleanBefore
	report.ProfileHash, report.CommandDefinitionHash, err = DefinitionHashes(root, profile)
	if err != nil {
		return finishReport(fail(report, "command_definition_unavailable"), started)
	}
	if report.Dirty && !allowDirty {
		return finishReport(fail(report, "dirty_workspace"), started)
	}

	profileCtx := ctx
	profileCancel := func() {}
	if profile == ProfileS5 {
		profileCtx, profileCancel = context.WithTimeout(ctx, s5ProfileTimeout-s5CleanupReserve)
	}
	defer profileCancel()
	for _, command := range commands {
		commandTimeout := command.Timeout
		if commandTimeout <= 0 {
			commandTimeout = 20 * time.Minute
		}
		checkStarted := time.Now().UTC()
		commandCtx, cancel := context.WithTimeout(profileCtx, commandTimeout)
		stdout, commandErr := executor.Run(commandCtx, root, command)
		timedOut := errors.Is(commandErr, context.DeadlineExceeded) || errors.Is(commandCtx.Err(), context.DeadlineExceeded)
		cancel()
		checkEnded := time.Now().UTC()
		termination, cleanup, diagnostics := executionMetadata(commandErr)
		check := Check{
			Name: checkName(command), Status: ResultPassed, Command: commandString(command), Criteria: command.Criteria,
			Evidence: command.Evidence, Artifacts: command.Artifacts,
			StartedAt: checkStarted.Format(time.RFC3339Nano), EndedAt: checkEnded.Format(time.RFC3339Nano),
			DurationMillis: checkEnded.Sub(checkStarted).Milliseconds(), TimeoutMillis: commandTimeout.Milliseconds(),
			TimedOut: timedOut, Termination: termination, Cleanup: cleanup, DiagnosticPaths: diagnostics,
		}
		if command.Native {
			native, parseErr := parseNativeOutput(stdout)
			if parseErr != nil {
				check.Status = ResultFailed
				report.Checks = append(report.Checks, check)
				return finishReport(fail(report, "native_result_invalid"), started)
			}
			report.Native = &native
			if native.Result == keyringnative.ResultFailed {
				check.Status = ResultFailed
			}
		}
		if commandErr != nil || check.Status == ResultFailed {
			check.Status = ResultFailed
			report.Checks = append(report.Checks, check)
			if errors.Is(profileCtx.Err(), context.DeadlineExceeded) {
				return finishReport(fail(report, "profile_deadline_exceeded"), started)
			}
			return finishReport(fail(report, check.Name+"_failed"), started)
		}
		report.Checks = append(report.Checks, check)
	}

	finalRevision, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil || finalRevision != report.Revision {
		return finishReport(fail(report, "acceptance_changed_revision"), started)
	}
	finalStatus, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil {
		return finishReport(fail(report, "git_status_unavailable"), started)
	}
	report.Dirty = finalStatus != ""
	report.CleanAfter = !report.Dirty
	if report.Dirty && !allowDirty {
		return finishReport(fail(report, "acceptance_changed_workspace"), started)
	}
	report.Reason = "all_checks_passed"
	return finishReport(report, started)
}

func Parse(contents []byte) (Report, error) {
	var report Report
	if err := strictjson.Decode(contents, &report, strictjson.Options{MaxBytes: 32768, MaxDepth: 8, RejectUnknownMembers: true}); err != nil {
		return Report{}, err
	}
	var schemaDocument any
	if err := json.Unmarshal(reportSchema, &schemaDocument); err != nil {
		return Report{}, fmt.Errorf("decode acceptance schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:mcp-gateway:acceptance", schemaDocument); err != nil {
		return Report{}, fmt.Errorf("load acceptance schema: %w", err)
	}
	schema, err := compiler.Compile("urn:mcp-gateway:acceptance")
	if err != nil {
		return Report{}, fmt.Errorf("compile acceptance schema: %w", err)
	}
	var instance any
	if err := json.Unmarshal(contents, &instance); err != nil {
		return Report{}, err
	}
	if err := schema.Validate(instance); err != nil {
		return Report{}, fmt.Errorf("validate acceptance schema: %w", err)
	}
	commands, err := ProfileCommands(report.Profile)
	if err != nil {
		return Report{}, err
	}
	profileHash, err := ProfileDefinitionHash(report.Profile)
	if err != nil || profileHash != report.ProfileHash {
		return Report{}, errors.New("acceptance profile hash does not match the closed profile")
	}
	if err := validateTiming(report.StartedAt, report.EndedAt, report.DurationMillis); err != nil {
		return Report{}, fmt.Errorf("invalid acceptance report timing: %w", err)
	}
	if len(report.Checks) > len(commands) {
		return Report{}, errors.New("acceptance report has more checks than its profile")
	}
	for index, check := range report.Checks {
		command := commands[index]
		if check.Name != command.CheckName || check.Command != commandString(command) || !reflect.DeepEqual(check.Criteria, command.Criteria) || !reflect.DeepEqual(check.Evidence, command.Evidence) || !reflect.DeepEqual(check.Artifacts, command.Artifacts) || check.TimeoutMillis != effectiveCommandTimeout(command).Milliseconds() {
			return Report{}, errors.New("acceptance check does not match the closed profile")
		}
		if err := validateTiming(check.StartedAt, check.EndedAt, check.DurationMillis); err != nil {
			return Report{}, fmt.Errorf("invalid acceptance check timing: %w", err)
		}
		if check.TimedOut && check.Status != ResultFailed {
			return Report{}, errors.New("timed out acceptance check must fail")
		}
		if check.Cleanup == "failed" && check.Status != ResultFailed {
			return Report{}, errors.New("acceptance cleanup failure must fail its check")
		}
	}
	switch report.Profile {
	case ProfileS3:
		if !reflect.DeepEqual(report.EvidenceManifest, contract.AcceptanceEvidenceManifest()) {
			return Report{}, errors.New("acceptance evidence manifest does not match the closed contract")
		}
		if len(report.ClauseManifest) != 0 {
			return Report{}, errors.New("S3 profile cannot contain an S4 clause manifest")
		}
	case ProfileS4:
		if !reflect.DeepEqual(report.EvidenceManifest, contract.S4AcceptanceEvidenceManifest()) {
			return Report{}, errors.New("S4 evidence manifest does not match the closed contract")
		}
		if !reflect.DeepEqual(report.ClauseManifest, contract.S4ClauseEvidenceManifest()) {
			return Report{}, errors.New("S4 clause manifest does not match the closed contract")
		}
	case ProfileS5:
		if !reflect.DeepEqual(report.EvidenceManifest, contract.S5AcceptanceEvidenceManifest()) {
			return Report{}, errors.New("S5 evidence manifest does not match the closed contract")
		}
		if !reflect.DeepEqual(report.ClauseManifest, contract.S5ClauseEvidenceManifest()) {
			return Report{}, errors.New("S5 clause manifest does not match the closed contract")
		}
	case ProfileS21:
		if len(report.EvidenceManifest) != 0 || len(report.ClauseManifest) != 0 {
			return Report{}, errors.New("S2.1 profile cannot contain a later evidence manifest")
		}
	}
	if err := validateEvidenceCoverage(commands, report.EvidenceManifest); err != nil {
		return Report{}, err
	}
	if report.Profile == ProfileS5 {
		owners := make(map[string]int)
		for _, command := range commands {
			for _, evidence := range command.Evidence {
				owners[evidence]++
			}
		}
		for evidence, count := range owners {
			if count != 1 {
				return Report{}, fmt.Errorf("S5 evidence %q must have exactly one command owner", evidence)
			}
		}
	}
	if err := validateClauseCoverage(commands, report.ClauseManifest); err != nil {
		return Report{}, err
	}
	if report.Result == ResultPassed {
		if report.Reason != "all_checks_passed" || len(report.Checks) != len(commands) || report.Native == nil || report.Native.Result == keyringnative.ResultFailed {
			return Report{}, errors.New("passed acceptance requires every check and nonfailed native evidence")
		}
		if report.DirtyPolicy == "required_clean" && (!report.CleanBefore || !report.CleanAfter || report.Dirty) {
			return Report{}, errors.New("passed acceptance requires a clean unchanged workspace")
		}
		for _, check := range report.Checks {
			if check.Status != ResultPassed {
				return Report{}, errors.New("passed acceptance cannot contain a failed check")
			}
		}
	}
	return report, nil
}

func parseNativeOutput(stdout []byte) (keyringnative.Result, error) {
	trimmed := bytes.TrimSpace(stdout)
	if native, err := keyringnative.Parse(trimmed); err == nil {
		return native, nil
	}
	var candidate []byte
	for _, line := range bytes.Split(trimmed, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 && line[0] == '{' {
			if candidate != nil {
				return keyringnative.Result{}, errors.New("native output contains multiple JSON documents")
			}
			candidate = append([]byte(nil), line...)
		}
	}
	if candidate == nil {
		return keyringnative.Result{}, errors.New("native output does not contain JSON evidence")
	}
	return keyringnative.Parse(candidate)
}

func effectiveCommandTimeout(command Command) time.Duration {
	if command.Timeout > 0 {
		return command.Timeout
	}
	return 20 * time.Minute
}

func validateEvidenceCoverage(commands []Command, manifest []contract.AcceptanceEvidence) error {
	mapped := make(map[string]bool)
	for _, command := range commands {
		for _, evidence := range command.Evidence {
			mapped[evidence] = true
		}
	}
	for _, criterion := range manifest {
		for _, evidence := range criterion.Evidence {
			if !mapped[evidence] {
				return fmt.Errorf("acceptance evidence %s for %s has no command", evidence, criterion.Criterion)
			}
		}
	}
	return nil
}

func validateClauseCoverage(commands []Command, manifest []contract.ClauseEvidence) error {
	mapped := make(map[string]bool)
	for _, command := range commands {
		for _, evidence := range command.Evidence {
			mapped[evidence] = true
		}
	}
	for _, clause := range manifest {
		for _, evidence := range clause.Evidence {
			if !mapped[evidence] {
				return fmt.Errorf("acceptance evidence %s for %s has no command", evidence, clause.Clause)
			}
		}
	}
	return nil
}

func fail(report Report, reason string) Report {
	report.Result = ResultFailed
	report.Reason = reason
	return report
}

func finishReport(report Report, started time.Time) Report {
	ended := time.Now().UTC()
	report.EndedAt = ended.Format(time.RFC3339Nano)
	report.DurationMillis = ended.Sub(started).Milliseconds()
	return report
}

func validateTiming(startedAt, endedAt string, durationMillis int64) error {
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return err
	}
	ended, err := time.Parse(time.RFC3339Nano, endedAt)
	if err != nil {
		return err
	}
	if ended.Before(started) || ended.Sub(started).Milliseconds() != durationMillis {
		return errors.New("timestamps and duration disagree")
	}
	return nil
}

func executionMetadata(err error) (string, string, []string) {
	termination := "none"
	cleanup := "passed"
	diagnostics := []string{}
	var failure *commandExecutionError
	if errors.As(err, &failure) {
		termination = failure.termination
		cleanup = failure.cleanup
		diagnostics = append(diagnostics, failure.diagnosticPaths...)
	}
	return termination, cleanup, diagnostics
}

func gitOutput(ctx context.Context, root string, arguments ...string) (string, error) {
	process := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...) //nolint:gosec // The CLI resolves root from Git and arguments are fixed internally.
	output, err := process.Output()
	return strings.TrimSpace(string(output)), err
}

func checkName(command Command) string {
	return command.CheckName
}

func commandString(command Command) string {
	return strings.Join(append([]string{command.Name}, command.Arguments...), " ")
}
