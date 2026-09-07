package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPurposeNamedLeafTargetDryRuns(t *testing.T) {
	root := purposeTargetModuleRoot(t)
	checks := map[string][]string{
		"test-stress":                    {"go run ./test/acceptance/cmd run-suite test-stress --count=20"},
		"test-keyring-native":            {"./test/keyring-native.sh"},
		"test-serve-temporary":           {"./test/serve-temporary.sh"},
		"test-frontend-development-node": {"npm --prefix .. run ui:test-dev"},
		"frontend-typecheck":             {"npm --prefix .. run ui:typecheck"},
		"frontend-build":                 {"npm --prefix .. run ui:build"},
		"frontend-verify-generated":      {"npm --prefix .. run ui:verify-generated"},
		"frontend-verify-supply-chain":   {"npm --prefix .. run ui:verify-supply-chain", "go run ./test/acceptance/cmd run-suite frontend-static-tests"},
		"frontend-audit":                 {"npm --prefix .. run ui:audit"},
	}
	for _, target := range []string{"test-unit", "test-integration", "test-harness", "test-material", "test-e2e", "test-security", "test-browser-workflows", "test-browser-privacy", "test-browser-visual", "test-browser-accessibility", "test-browser-cross", "test-frontend-development-browser"} {
		checks[target] = []string{"go run ./test/acceptance/cmd run-suite " + target}
	}
	for target, required := range checks {
		t.Run(target, func(t *testing.T) {
			output := purposeTargetDryRun(t, root, target)
			for _, fragment := range required {
				assert.Contains(t, output, fragment)
			}
			assert.NotRegexp(t, `(?:test|accept)-(?:s[1-6]|task|milestone)`, output)
		})
	}
}

func TestPurposeNamedDeveloperAggregatesAreDisjoint(t *testing.T) {
	root := purposeTargetModuleRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	assert.Contains(t, string(makefile), ".NOTPARALLEL: test test-browser test-frontend-development")

	var wantTest []string
	for _, target := range []string{"test-unit", "test-integration", "test-harness", "test-material", "test-serve-temporary"} {
		wantTest = append(wantTest, purposeTargetDryRunLines(t, root, target)...)
	}
	assert.Equal(t, wantTest, purposeTargetDryRunLines(t, root, "test"))

	assert.Equal(t, []string{"go run ./test/acceptance/cmd run-suite test-browser"}, purposeTargetDryRunLines(t, root, "test-browser"))

	var development []string
	for _, target := range []string{"test-frontend-development-node", "test-frontend-development-browser"} {
		development = append(development, purposeTargetDryRunLines(t, root, target)...)
	}
	assert.Equal(t, development, purposeTargetDryRunLines(t, root, "test-frontend-development"))
}

func TestPurposeNamedBuildTagsKeepLeafTiersDisjoint(t *testing.T) {
	root := purposeTargetModuleRoot(t)
	integrationFiles, err := filepath.Glob(filepath.Join(root, "internal", "*", "*_integration_test.go"))
	require.NoError(t, err)
	require.NotEmpty(t, integrationFiles)
	for _, path := range integrationFiles {
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.True(t, strings.HasPrefix(string(contents), "//go:build integration\n"), path)
	}

	handler, err := os.ReadFile(filepath.Join(root, "internal", "api", "handler_test.go"))
	require.NoError(t, err)
	assert.NotContains(t, string(handler), "func TestStaticSupplyChain")
	frontend, err := os.ReadFile(filepath.Join(root, "internal", "api", "static_supply_chain_frontend_test.go"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(frontend), "//go:build frontend\n"))
	assert.Contains(t, string(frontend), "func TestStaticSupplyChain")
}

func TestPurposeNamedRootForwardingAndOwnership(t *testing.T) {
	moduleRoot := purposeTargetModuleRoot(t)
	repositoryRoot := filepath.Dir(moduleRoot)
	contents, err := os.ReadFile(filepath.Join(repositoryRoot, "Makefile"))
	require.NoError(t, err)
	makefile := string(contents)

	forwarded := []string{
		"test-browser", "test-frontend-development", "frontend-typecheck", "frontend-build",
		"frontend-verify-generated", "frontend-verify-supply-chain", "frontend-audit",
	}
	for _, target := range forwarded {
		assert.Equal(t, "\t$(MAKE) -C mcp-gateway "+target, purposeMakeTargetRecipe(makefile, target), target)
		command := exec.Command("make", "-n", "-C", repositoryRoot, target)
		output, dryRunErr := command.CombinedOutput()
		require.NoError(t, dryRunErr, "%s: %s", target, output)
		assert.Contains(t, string(output), "make -C mcp-gateway "+target, target)
	}
	assert.Equal(t, "\t$(MAKE) -C mcp-gateway accept REPORT=\"$(REPORT)\"", purposeMakeTargetRecipe(makefile, "accept"))
	assert.Equal(t, "\t$(MAKE) -C mcp-gateway adopt-acceptance-report REPORT=\"$(REPORT)\" ADOPTION=\"$(ADOPTION)\"", purposeMakeTargetRecipe(makefile, "adopt-acceptance-report"))
	assert.Equal(t, "\t$(MAKE) -C mcp-gateway qualify-external-evidence", purposeMakeTargetRecipe(makefile, "qualify-external-evidence"))
	assert.NotContains(t, purposeMakeTargetRecipe(makefile, "check-other-tools"), "mcp-gateway")
	assert.Contains(t, makefile, "OTHER_TOOLS := $(filter-out mcp-gateway,$(TOOLS))")
}

func TestPurposeNamedTargetsPublishOnlyFinalAcceptanceInterface(t *testing.T) {
	root := purposeTargetModuleRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	text := string(makefile)
	for _, target := range []string{"accept", "adopt-acceptance-report", "qualify-external-evidence", "help"} {
		assert.Contains(t, text, target+":", target)
	}
	help := purposeTargetDryRun(t, root, "help")
	for _, fragment := range []string{"accept REPORT=<absolute-path>", "adopt-acceptance-report REPORT=<absolute-path> ADOPTION=<absolute-path>", "qualify-external-evidence"} {
		assert.Contains(t, help, fragment)
	}
	for _, removed := range []string{
		"test-integration-s5", "test-security-s5", "test-stress-s5", "test-task-s6", "test-milestone-s6",
		"test-frontend-s6", "test-unit-s6", "test-integration-s6", "test-browser-s6-workflows", "test-browser-s6-visual", "test-browser-s6-a11y", "test-browser-s6-cross",
		"test-cli-e2e-s6", "test-cli-usability-e2e", "test-security-s6", "accept-s6", "accept-s5", "accept-s4", "accept-s3", "accept-s2-1", "qualify-external-s6",
	} {
		assert.NotContains(t, text, removed+":", removed)
		command := exec.Command("make", "-n", "-C", root, removed)
		assert.Error(t, command.Run(), removed)
	}
	guidance, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	for _, target := range []string{"test-security", "test-stress", "test-browser", "frontend-typecheck", "frontend-build", "frontend-verify-generated", "frontend-verify-supply-chain", "frontend-audit"} {
		assert.Contains(t, string(guidance), "make "+target, target)
	}
}

func TestPurposeNamedStructuredOutputIsOptIn(t *testing.T) {
	root := purposeTargetModuleRoot(t)
	for _, target := range []string{"test-unit", "test-integration", "test-harness", "test-material", "test-browser", "test-e2e", "test-security", "test-frontend-development-browser", "frontend-verify-supply-chain"} {
		t.Run(target, func(t *testing.T) {
			assert.NotContains(t, purposeTargetDryRun(t, root, target), "--json")
			output := purposeTargetDryRun(t, root, target, "TEST_JSON=1")
			found := false
			for _, line := range strings.Split(output, "\n") {
				if strings.HasPrefix(line, "go run ./test/acceptance/cmd run-suite ") {
					found = true
					assert.True(t, strings.HasSuffix(strings.TrimSpace(line), "--json"), line)
				}
			}
			assert.True(t, found)
		})
	}
}

func purposeTargetDryRun(t *testing.T, root, target string, overrides ...string) string {
	t.Helper()
	runner, err := testutil.NewBinaryRunner(5*time.Second, 64*1024)
	require.NoError(t, err)
	arguments := append([]string{"-n", "-C", root, target, "TEST_JSON=0"}, overrides...)
	result, err := runner.Run(t.Context(), "make", arguments...)
	output := string(result.Stdout) + string(result.Stderr)
	require.NoError(t, err, output)
	require.False(t, result.StdoutTruncated)
	require.False(t, result.StderrTruncated)
	return output
}

func purposeTargetDryRunLines(t *testing.T, root, target string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(purposeTargetDryRun(t, root, target)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "make[") || strings.HasPrefix(line, "make: ") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func purposeTargetModuleRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func purposeMakeTargetRecipe(makefile, target string) string {
	start := strings.Index(makefile, "\n"+target+":")
	if start < 0 {
		return ""
	}
	rest := makefile[start+1:]
	if end := strings.Index(rest, "\n\n"); end >= 0 {
		rest = rest[:end]
	}
	lines := strings.Split(rest, "\n")
	if len(lines) < 2 {
		return ""
	}
	return strings.Join(lines[1:], "\n")
}
