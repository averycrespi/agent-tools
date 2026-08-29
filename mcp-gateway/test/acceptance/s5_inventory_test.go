package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const s5IntegrationSelector = `^(TestS5Integration.*|TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline|TestBusyBeyondDeadlineLatchesMutationAcrossRestart|TestRepositoryRetainsNewest4096ByMonotonicSequence|TestRepositoryRollsBackEvictionWhenInsertFails|TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss)$`

var s5IntegrationPackages = []string{
	"./internal/storage", "./internal/backup", "./internal/grantrequests", "./internal/authorization",
	"./internal/catalog", "./internal/invocation", "./internal/composition",
}

var s5StressPackages = []string{"./internal/grantrequests", "./internal/selfservice", "./internal/composition"}

func TestS5TargetInventory(t *testing.T) {
	gatewayMakefile, err := os.ReadFile(filepath.Join(repositoryRoot(t), "mcp-gateway", "Makefile"))
	require.NoError(t, err)
	gateway := string(gatewayMakefile)
	for _, target := range []string{"test-unit", "test-integration-s5", "test-security-s5", "test-stress-s5", "test-e2e", "verify"} {
		assert.Contains(t, gateway, "\n"+target+":", target)
	}
	assert.Contains(t, gateway, "\tgo test -race -count=1 -timeout=90s ./...")
	assert.Contains(t, gateway, "\tgo test -race -count=1 -tags=integration -timeout=30s -run '"+strings.ReplaceAll(s5IntegrationSelector, "$", "$$")+"' "+strings.Join(s5IntegrationPackages, " "))
	assert.Contains(t, gateway, "\tgo test -race -count=1 -tags=security -timeout=30s ./test/security/...")
	assert.Contains(t, gateway, "\tgo test -race -tags=stress -count=$(STRESS_COUNT) -timeout=2m")
	assert.Contains(t, gateway, "\tgo test -race -tags=stress -count=$(STRESS_COUNT) -timeout=90s")
	assert.Contains(t, gateway, "\tgo test -race -count=1 -tags=e2e -timeout=2m ./test/e2e/...")
	for _, command := range []string{"\tgo mod tidy -diff", "\tgo mod verify", "\tgo tool golangci-lint fmt --diff", "\tgo tool golangci-lint run ./..."} {
		assert.Contains(t, gateway, command)
	}

	rootMakefile, err := os.ReadFile(filepath.Join(repositoryRoot(t), "Makefile"))
	require.NoError(t, err)
	root := string(rootMakefile)
	assert.Contains(t, root, "OTHER_TOOLS := mcp-broker sandbox-manager local-git-mcp local-gomod-proxy telegram-mcp http-broker")
	assert.Contains(t, root, "\ncheck-other-tools:")
	assert.NotContains(t, targetRecipe(root, "check-other-tools"), "mcp-gateway")
}

func TestS5SelectorManifest(t *testing.T) {
	assert.Equal(t, []string{
		"TestS5StressApprovalCancellationPolicyLinearization",
		"TestS5StressConcurrentSemanticDeduplication",
		"TestS5StressLocalInvocationDrainCleanup",
		"TestS5StressLostLocalMutationResponseDeduplicatesExplicitRetry",
		"TestS5StressSyntheticSnapshotPagination",
	}, S5StressTestManifest)
	assert.Equal(t, []string{
		"TestS5SecurityAcceptanceReportSinks",
		"TestS5SecurityDurableSinkCanaries",
		"TestS5SecurityStaticSinkClosure",
	}, S5SecurityTestManifest)

	selector := regexp.MustCompile(s5IntegrationSelector)
	var selected []string
	for _, test := range discoverTests(t, repositoryRoot(t), s5IntegrationPackages) {
		if selector.MatchString(test) {
			selected = append(selected, test)
		}
	}
	sort.Strings(selected)
	assert.Equal(t, S5IntegrationTestManifest, selected)

	var stress []string
	for _, test := range discoverTests(t, repositoryRoot(t), s5StressPackages) {
		if strings.HasPrefix(test, "TestS5Stress") {
			stress = append(stress, test)
		}
	}
	sort.Strings(stress)
	assert.Equal(t, S5StressTestManifest, stress)

	var security []string
	for _, test := range discoverTests(t, repositoryRoot(t), []string{"./test/security"}) {
		if strings.HasPrefix(test, "TestS5Security") {
			security = append(security, test)
		}
	}
	sort.Strings(security)
	assert.Equal(t, S5SecurityTestManifest, security)
}

func TestCLIUsabilityTarget(t *testing.T) {
	const selector = `^(TestCLI(FirstRun|XDGAndOverrides|ServeOutputPhases|AutomaticBearerSelection|CredentialFailureProblems|OutputMatrix|CommandErrors|HelpTree)|TestS6CLI(StatusInvocations|ServerCatalogReads|ServerCreateUpdate|ServerDelete|ServerOperations|ServerCredentials|AuthFlows|AdminCredentials|Backups|Principals|PrincipalCredentials|Grants|GrantRequests))$`
	makefile, err := os.ReadFile(filepath.Join(repositoryRoot(t), "mcp-gateway", "Makefile"))
	require.NoError(t, err)
	recipe := targetRecipe(string(makefile), "test-cli-usability-e2e")
	assert.Equal(t, 1, strings.Count(recipe, "go test "))
	assert.Contains(t, recipe, "-race -count=1 -tags=e2e -timeout=2m")
	assert.Contains(t, recipe, "-run '"+strings.ReplaceAll(selector, "$", "$$")+"'")
	assert.Equal(t, 1, strings.Count(recipe, "./test/e2e"))
	assert.NotContains(t, recipe, "$(MAKE)")

	matcher := regexp.MustCompile(selector)
	selected := make([]string, 0)
	for _, name := range discoverTests(t, repositoryRoot(t), []string{"./test/e2e"}) {
		if matcher.MatchString(name) {
			selected = append(selected, name)
		}
	}
	sort.Strings(selected)
	assert.Equal(t, []string{
		"TestCLIAutomaticBearerSelection", "TestCLICommandErrors", "TestCLICredentialFailureProblems", "TestCLIFirstRun",
		"TestCLIHelpTree", "TestCLIOutputMatrix", "TestCLIServeOutputPhases", "TestCLIXDGAndOverrides",
		"TestS6CLIAdminCredentials", "TestS6CLIAuthFlows", "TestS6CLIBackups", "TestS6CLIGrantRequests", "TestS6CLIGrants",
		"TestS6CLIPrincipalCredentials", "TestS6CLIPrincipals", "TestS6CLIServerCatalogReads", "TestS6CLIServerCreateUpdate",
		"TestS6CLIServerCredentials", "TestS6CLIServerDelete", "TestS6CLIServerOperations", "TestS6CLIStatusInvocations",
	}, selected)
}

func TestS5PackageMultiplicity(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(repositoryRoot(t), "mcp-gateway", "Makefile"))
	require.NoError(t, err)
	text := string(makefile)
	for _, target := range []string{"test-unit", "test-integration-s5", "test-e2e"} {
		recipe := targetRecipe(text, target)
		assert.Equal(t, 1, strings.Count(recipe, "go test "), target)
		assert.NotContains(t, recipe, "$(MAKE)")
	}
	integration := targetRecipe(text, "test-integration-s5")
	for _, packagePath := range s5IntegrationPackages {
		assert.Equal(t, 1, strings.Count(integration, packagePath), packagePath)
	}
	assert.NotContains(t, targetRecipe(text, "test-unit"), "-tags=")
	assert.NotContains(t, targetRecipe(text, "test-e2e"), "-count=2")
	security := targetRecipe(text, "test-security-s5")
	assert.Equal(t, 1, strings.Count(security, "go test "))
	assert.Contains(t, security, "-count=1 -tags=security")
	assert.Equal(t, 1, strings.Count(security, "./test/security/..."))
	stress := targetRecipe(text, "test-stress-s5")
	assert.Equal(t, 3, strings.Count(stress, "go test "))
	assert.Equal(t, 3, strings.Count(stress, "-count=$(STRESS_COUNT)"))
	assert.NotContains(t, stress, "./...")
}

func targetRecipe(makefile, target string) string {
	start := strings.Index(makefile, "\n"+target+":")
	if start < 0 {
		return ""
	}
	rest := makefile[start+1:]
	if end := strings.Index(rest, "\n\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}

func discoverTests(t *testing.T, root string, packages []string) []string {
	t.Helper()
	var names []string
	for _, packagePath := range packages {
		files, err := filepath.Glob(filepath.Join(root, "mcp-gateway", strings.TrimPrefix(packagePath, "./"), "*_test.go"))
		require.NoError(t, err)
		for _, path := range files {
			parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			require.NoError(t, parseErr)
			for _, declaration := range parsed.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if ok && function.Recv == nil && strings.HasPrefix(function.Name.Name, "Test") {
					names = append(names, function.Name.Name)
				}
			}
		}
	}
	return names
}
