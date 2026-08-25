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
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/averycrespi/agent-tools/mcp-gateway/test/keyringnative"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	ResultPassed = "passed"
	ResultFailed = "failed"
)

type Check struct {
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Command   string   `json:"command"`
	Criteria  []string `json:"criteria"`
	Artifacts []string `json:"artifacts"`
}

type Report struct {
	SchemaVersion int                   `json:"schema_version"`
	Result        string                `json:"result"`
	Revision      string                `json:"revision"`
	Dirty         bool                  `json:"dirty"`
	DirtyPolicy   string                `json:"dirty_policy"`
	Reason        string                `json:"reason"`
	Checks        []Check               `json:"checks"`
	Native        *keyringnative.Result `json:"native,omitempty"`
}

type Command struct {
	Name      string
	Arguments []string
	Criteria  []string
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
	return []Command{
		{Name: "go", Arguments: []string{"-C", "mcp-gateway", "test", "-race", "./internal/composition", "./internal/downstream", "./internal/oauth", "./internal/contract", "./internal/backup", "./internal/keyring", "./test/material", "./test/keyringnative"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-6"}, Artifacts: []string{"mcp-gateway/internal/composition/source_test.go", "mcp-gateway/internal/downstream/source_test.go", "mcp-gateway/internal/oauth/source_test.go", "mcp-gateway/internal/contract/documentation_test.go"}},
		{Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-integration"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-6"}, Artifacts: []string{"mcp-gateway/internal/composition", "mcp-gateway/internal/servers"}},
		{Name: "make", Arguments: []string{"-C", "mcp-gateway", "test-e2e"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6"}, Artifacts: []string{"mcp-gateway/test/e2e"}},
		{Name: "make", Arguments: []string{"-C", "mcp-gateway", "audit"}, Criteria: []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}},
		{Name: "go", Arguments: []string{"-C", "mcp-gateway", "tool", "govulncheck", "./..."}, Criteria: []string{"AC-6"}, Artifacts: []string{"mcp-gateway/go.mod", "mcp-gateway/go.sum"}},
		{Name: "mcp-gateway/test/keyring-native.sh", Criteria: []string{"AC-1", "AC-6"}, Artifacts: []string{"mcp-gateway/test/keyringnative/result.schema.json", "mcp-gateway/test/material/material_test.go"}, Native: true},
		{Name: "make", Arguments: []string{"check"}, Criteria: []string{"AC-6"}, Artifacts: []string{"Makefile", "package.json"}},
		{Name: "git", Arguments: []string{"diff", "--check"}, Criteria: []string{"AC-6"}, Artifacts: []string{"mcp-gateway/README.md", "mcp-gateway/DESIGN.md", "mcp-gateway/CLAUDE.md"}},
	}
}

func Run(ctx context.Context, root string, executor Executor, allowDirty bool) Report {
	report := Report{SchemaVersion: 1, Result: ResultPassed, DirtyPolicy: "required_clean", Checks: []Check{}}
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

	for _, command := range Commands() {
		commandCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		stdout, commandErr := executor.Run(commandCtx, root, command)
		cancel()
		check := Check{Name: checkName(command), Status: ResultPassed, Command: commandString(command), Criteria: command.Criteria, Artifacts: command.Artifacts}
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
	if err := compiler.AddResource("urn:mcp-gateway:s2-1-acceptance", schemaDocument); err != nil {
		return Report{}, fmt.Errorf("load acceptance schema: %w", err)
	}
	schema, err := compiler.Compile("urn:mcp-gateway:s2-1-acceptance")
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
	if report.Result == ResultPassed {
		if report.Reason != "all_checks_passed" || len(report.Checks) != len(Commands()) || report.Native == nil || report.Native.Result == keyringnative.ResultFailed {
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
	if command.Native {
		return "native_keyring"
	}
	if command.Name == "go" && len(command.Arguments) > 2 && command.Arguments[2] == "test" {
		return "focused_race_guards"
	}
	if command.Name == "go" {
		return "unsuppressed_govulncheck"
	}
	if command.Name == "git" {
		return "diff_check"
	}
	if command.Name == "make" && len(command.Arguments) == 1 {
		return "repository_check"
	}
	return strings.ReplaceAll(command.Arguments[2], "-", "_")
}

func commandString(command Command) string {
	return strings.Join(append([]string{command.Name}, command.Arguments...), " ")
}
