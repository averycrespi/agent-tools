package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPurposeNamedLeafTargetDryRuns(t *testing.T) {
	root := purposeTargetModuleRoot(t)
	checks := map[string][]string{
		"test-unit":                         {"go test -race -count=1 -timeout=90s ./..."},
		"test-integration":                  {"-count=1", "-tags=integration", "Test.*Integration", "TestRestoreAcceptedSchemaLineages"},
		"test-e2e":                          {"-count=1", "-tags=e2e", "./test/e2e/..."},
		"test-security":                     {"-count=1", "-tags=security", "./test/security/..."},
		"test-stress":                       {"TestConcurrentSemanticDeduplicationStress", "TestApprovalCancellationPolicyLinearizationStress", "TestSyntheticSnapshotPaginationStress", "TestLostLocalMutationResponseDeduplicatesExplicitRetryStress", "TestLocalInvocationDrainCleanupStress"},
		"test-keyring-native":               {"./test/keyring-native.sh"},
		"test-browser-workflows":            {"TestBrowser(Protocol|FragmentStorage|AuthenticationEpoch", "-tags=e2e,browser"},
		"test-browser-privacy":              {"TestBrowserSecretStoragePrivacy"},
		"test-browser-visual":               {"TestBrowserVisualResponsiveMatrix"},
		"test-browser-accessibility":        {"TestBrowserAccessibility"},
		"test-browser-cross":                {"TestBrowserCrossCompatibility"},
		"test-frontend-development-node":    {"npm --prefix .. run ui:test-dev"},
		"test-frontend-development-browser": {"TestFrontendDevelopment(LiveReload|ControlPlane)"},
		"frontend-typecheck":                {"npm --prefix .. run ui:typecheck"},
		"frontend-build":                    {"npm --prefix .. run ui:build"},
		"frontend-verify-generated":         {"npm --prefix .. run ui:verify-generated"},
		"frontend-verify-supply-chain":      {"npm --prefix .. run ui:verify-supply-chain", "-tags=frontend", "TestStaticSupplyChain"},
		"frontend-audit":                    {"npm --prefix .. run ui:audit"},
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

	unit := purposeTargetDryRunLines(t, root, "test-unit")
	integration := purposeTargetDryRunLines(t, root, "test-integration")
	assert.Equal(t, append(append([]string(nil), unit...), integration...), purposeTargetDryRunLines(t, root, "test"))

	var browser []string
	for _, target := range []string{"test-browser-workflows", "test-browser-privacy", "test-browser-visual", "test-browser-accessibility", "test-browser-cross"} {
		browser = append(browser, purposeTargetDryRunLines(t, root, target)...)
	}
	assert.Equal(t, browser, purposeTargetDryRunLines(t, root, "test-browser"))

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
	assert.NotContains(t, purposeMakeTargetRecipe(makefile, "check-other-tools"), "mcp-gateway")
	assert.Contains(t, makefile, "OTHER_TOOLS := mcp-broker sandbox-manager local-git-mcp local-gomod-proxy telegram-mcp http-broker")
}

func TestPurposeNamedTargetsRetainUnpublishedLegacyEntryPoints(t *testing.T) {
	root := purposeTargetModuleRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	text := string(makefile)
	for _, target := range []string{
		"test-integration-s5", "test-security-s5", "test-stress-s5", "test-task-s6", "test-milestone-s6",
		"test-frontend-s6", "test-unit-s6", "test-integration-s6", "test-browser-s6-workflows", "accept-s6",
	} {
		assert.Contains(t, text, target+":", target)
	}
	guidance, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	for _, target := range []string{"test-security", "test-stress", "test-browser", "frontend-typecheck", "frontend-build", "frontend-verify-generated", "frontend-verify-supply-chain", "frontend-audit"} {
		assert.Contains(t, string(guidance), "make "+target, target)
	}
}

func purposeTargetDryRun(t *testing.T, root, target string) string {
	t.Helper()
	command := exec.Command("make", "-n", "-C", root, target)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return string(output)
}

func purposeTargetDryRunLines(t *testing.T, root, target string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(purposeTargetDryRun(t, root, target)), "\n") {
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
