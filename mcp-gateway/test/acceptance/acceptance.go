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
	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	ResultPassed = "passed"
	ResultFailed = "failed"

	ProfileS21 Profile = "s2_1"
	ProfileS3  Profile = "s3"
	ProfileS4  Profile = "s4"
)

type Profile string

type Check struct {
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Command   string   `json:"command"`
	Criteria  []string `json:"criteria"`
	Evidence  []string `json:"evidence,omitempty"`
	Artifacts []string `json:"artifacts"`
}

type Report struct {
	SchemaVersion    int                           `json:"schema_version"`
	Profile          Profile                       `json:"profile"`
	Result           string                        `json:"result"`
	Revision         string                        `json:"revision"`
	Dirty            bool                          `json:"dirty"`
	DirtyPolicy      string                        `json:"dirty_policy"`
	Reason           string                        `json:"reason"`
	Checks           []Check                       `json:"checks"`
	EvidenceManifest []contract.AcceptanceEvidence `json:"evidence_manifest,omitempty"`
	ClauseManifest   []contract.ClauseEvidence     `json:"clause_manifest,omitempty"`
	Native           *keyringnative.Result         `json:"native,omitempty"`
}

type Command struct {
	CheckName string
	Name      string
	Arguments []string
	Criteria  []string
	Evidence  []string
	Artifacts []string
	Native    bool
}

type Executor interface {
	Run(context.Context, string, Command) ([]byte, error)
}

type OSExecutor struct{}

func (OSExecutor) Run(ctx context.Context, root string, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Name, command.Arguments...) //nolint:gosec // Commands come only from the closed acceptance table.
	process.Dir = root
	process.Stderr = os.Stderr
	if command.Native {
		var stdout bytes.Buffer
		process.Stdout = &stdout
		err := process.Run()
		_, _ = os.Stderr.Write(stdout.Bytes())
		return stdout.Bytes(), err
	}
	process.Stdout = os.Stderr
	return nil, process.Run()
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
	default:
		return nil, fmt.Errorf("unknown acceptance profile %q", profile)
	}
}

func Run(ctx context.Context, root string, executor Executor, allowDirty bool) Report {
	return RunProfile(ctx, root, executor, ProfileS21, allowDirty)
}

func RunProfile(ctx context.Context, root string, executor Executor, profile Profile, allowDirty bool) Report {
	report := Report{SchemaVersion: 2, Profile: profile, Result: ResultPassed, DirtyPolicy: "required_clean", Checks: []Check{}}
	commands, err := ProfileCommands(profile)
	if err != nil {
		return fail(report, "unknown_profile")
	}
	switch profile {
	case ProfileS3:
		report.EvidenceManifest = contract.AcceptanceEvidenceManifest()
	case ProfileS4:
		report.EvidenceManifest = contract.S4AcceptanceEvidenceManifest()
		report.ClauseManifest = contract.S4ClauseEvidenceManifest()
	}
	if allowDirty {
		report.DirtyPolicy = "allowed"
	}
	revision, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return fail(report, "git_revision_unavailable")
	}
	report.Revision = revision
	status, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil {
		return fail(report, "git_status_unavailable")
	}
	report.Dirty = status != ""
	if report.Dirty && !allowDirty {
		return fail(report, "dirty_workspace")
	}

	for _, command := range commands {
		commandCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		stdout, commandErr := executor.Run(commandCtx, root, command)
		cancel()
		check := Check{Name: checkName(command), Status: ResultPassed, Command: commandString(command), Criteria: command.Criteria, Evidence: command.Evidence, Artifacts: command.Artifacts}
		if command.Native {
			native, parseErr := keyringnative.Parse(bytes.TrimSpace(stdout))
			if parseErr != nil {
				check.Status = ResultFailed
				report.Checks = append(report.Checks, check)
				return fail(report, "native_result_invalid")
			}
			report.Native = &native
			if native.Result == keyringnative.ResultFailed {
				check.Status = ResultFailed
			}
		}
		if commandErr != nil || check.Status == ResultFailed {
			check.Status = ResultFailed
			report.Checks = append(report.Checks, check)
			return fail(report, check.Name+"_failed")
		}
		report.Checks = append(report.Checks, check)
	}

	finalStatus, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil {
		return fail(report, "git_status_unavailable")
	}
	report.Dirty = finalStatus != ""
	if report.Dirty && !allowDirty {
		return fail(report, "acceptance_changed_workspace")
	}
	report.Reason = "all_checks_passed"
	return report
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
	if len(report.Checks) > len(commands) {
		return Report{}, errors.New("acceptance report has more checks than its profile")
	}
	for index, check := range report.Checks {
		command := commands[index]
		if check.Name != command.CheckName || check.Command != commandString(command) || !reflect.DeepEqual(check.Criteria, command.Criteria) || !reflect.DeepEqual(check.Evidence, command.Evidence) || !reflect.DeepEqual(check.Artifacts, command.Artifacts) {
			return Report{}, errors.New("acceptance check does not match the closed profile")
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
	case ProfileS21:
		if len(report.EvidenceManifest) != 0 || len(report.ClauseManifest) != 0 {
			return Report{}, errors.New("S2.1 profile cannot contain a later evidence manifest")
		}
	}
	if err := validateEvidenceCoverage(commands, report.EvidenceManifest); err != nil {
		return Report{}, err
	}
	if err := validateClauseCoverage(commands, report.ClauseManifest); err != nil {
		return Report{}, err
	}
	if report.Result == ResultPassed {
		if report.Reason != "all_checks_passed" || len(report.Checks) != len(commands) || report.Native == nil || report.Native.Result == keyringnative.ResultFailed {
			return Report{}, errors.New("passed acceptance requires every check and nonfailed native evidence")
		}
		for _, check := range report.Checks {
			if check.Status != ResultPassed {
				return Report{}, errors.New("passed acceptance cannot contain a failed check")
			}
		}
	}
	return report, nil
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
