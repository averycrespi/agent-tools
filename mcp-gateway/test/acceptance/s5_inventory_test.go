package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	s5IntegrationSelector    = `^(TestS5Integration.*|TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline|TestBusyBeyondDeadlineLatchesMutationAcrossRestart|TestRepositoryRetainsNewest4096ByMonotonicSequence|TestRepositoryRollsBackEvictionWhenInsertFails|TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss)$`
	cliUsabilityE2ESelector  = `^(TestCLI(FirstRun|XDGAndOverrides|ServeOutputPhases|AutomaticBearerSelection|CredentialFailureProblems|OutputMatrix|CommandErrors|HelpTree)|TestS6CLI(StatusInvocations|ServerCatalogReads|ServerCreateUpdate|ServerDelete|ServerOperations|ServerCredentials|AuthFlows|AdminCredentials|Backups|Principals|PrincipalCredentials|Grants|GrantRequests))$`
	cliSourceSelector        = `^(TestProductionSourceOwnershipGuards|TestCLIControlBoundary)$`
	cliDocumentationSelector = `^(TestCLIUsabilityDocumentationDrift|TestReadmeRelativeLinksResolve|TestS6DocumentationDrift)$`
	cliIntegrationSelector   = `^(TestS6InvocationRepositoryReads|TestS5Integration.*|TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline|TestBusyBeyondDeadlineLatchesMutationAcrossRestart|TestRepositoryRetainsNewest4096ByMonotonicSequence|TestRepositoryRollsBackEvictionWhenInsertFails|TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss)$`
)

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
	makefile, err := os.ReadFile(filepath.Join(repositoryRoot(t), "mcp-gateway", "Makefile"))
	require.NoError(t, err)
	recipe := targetRecipe(string(makefile), "test-cli-usability-e2e")
	assert.Equal(t, 1, strings.Count(recipe, "go test "))
	assert.Contains(t, recipe, "-race -count=1 -tags=e2e -timeout=2m")
	assert.Contains(t, recipe, "-run '"+strings.ReplaceAll(cliUsabilityE2ESelector, "$", "$$")+"'")
	assert.Equal(t, 1, strings.Count(recipe, "./test/e2e"))
	assert.NotContains(t, recipe, "$(MAKE)")

	matcher := regexp.MustCompile(cliUsabilityE2ESelector)
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

func TestCLIUsabilityFinalOwnerInventory(t *testing.T) {
	type owner struct {
		id       string
		class    string
		command  string
		target   string
		packages []string
		selector string
	}
	owners := []owner{
		{id: "cli_unit", class: "unit", command: "make -C mcp-gateway test-unit-s6", target: "test-unit-s6", packages: []string{"./..."}},
		{id: "cli_integration", class: "integration", command: "make -C mcp-gateway test-integration-s6", target: "test-integration-s6", packages: s5IntegrationPackages, selector: cliIntegrationSelector},
		{id: "cli_e2e", class: "e2e", command: "make -C mcp-gateway test-cli-usability-e2e", target: "test-cli-usability-e2e", packages: []string{"./test/e2e"}, selector: cliUsabilityE2ESelector},
		{id: "cli_source", class: "source", command: "go -C mcp-gateway test -race -count=1 -timeout=30s -run '" + cliSourceSelector + "' ./internal/composition ./cmd/mcp-gateway", packages: []string{"./internal/composition", "./cmd/mcp-gateway"}, selector: cliSourceSelector},
		{id: "cli_security", class: "security", command: "go -C mcp-gateway tool govulncheck ./...", packages: []string{"./..."}},
		{id: "cli_documentation", class: "documentation", command: "go -C mcp-gateway test -race -count=1 -timeout=60s -run '" + cliDocumentationSelector + "' ./internal/contract", packages: []string{"./internal/contract"}, selector: cliDocumentationSelector},
	}
	classes := make(map[string]string, len(owners))
	ids := make(map[string]struct{}, len(owners))
	for _, entry := range owners {
		assert.NotEmpty(t, entry.command, entry.id)
		assert.NotContains(t, entry.command, "-count=2", entry.id)
		_, duplicateID := ids[entry.id]
		assert.False(t, duplicateID, entry.id)
		ids[entry.id] = struct{}{}
		_, duplicateClass := classes[entry.class]
		assert.False(t, duplicateClass, entry.class)
		classes[entry.class] = entry.id
	}
	assert.Equal(t, map[string]string{
		"unit": "cli_unit", "integration": "cli_integration", "e2e": "cli_e2e", "source": "cli_source",
		"security": "cli_security", "documentation": "cli_documentation",
	}, classes)

	makefileBytes, err := os.ReadFile(filepath.Join(repositoryRoot(t), "mcp-gateway", "Makefile"))
	require.NoError(t, err)
	makefile := string(makefileBytes)
	for _, entry := range owners {
		if entry.target == "" {
			continue
		}
		assert.Equal(t, 1, strings.Count(makefile, "\n"+entry.target+":"), entry.target)
		recipe := targetRecipe(makefile, entry.target)
		assert.Equal(t, 1, strings.Count(recipe, "go test "), entry.target)
		assert.Contains(t, recipe, "-race -count=1", entry.target)
		assert.NotContains(t, recipe, "$(MAKE)", entry.target)
		for _, packagePath := range entry.packages {
			assert.Equal(t, 1, strings.Count(recipe, packagePath), entry.target+" "+packagePath)
		}
		if entry.selector != "" {
			assert.Contains(t, recipe, "-run '"+strings.ReplaceAll(entry.selector, "$", "$$")+"'", entry.target)
		}
	}
	assert.Contains(t, targetRecipe(makefile, "test-unit-s6"), "-timeout=110s ./...")
	assert.Contains(t, targetRecipe(makefile, "test-integration-s6"), "-tags=integration -timeout=60s")
	assert.Contains(t, targetRecipe(makefile, "test-cli-usability-e2e"), "-tags=e2e -timeout=2m")

	selectedSource := selectedTests(t, cliSourceSelector, []string{"./internal/composition", "./cmd/mcp-gateway"})
	assert.Equal(t, []string{"TestCLIControlBoundary", "TestProductionSourceOwnershipGuards"}, selectedSource)
	selectedDocumentation := selectedTests(t, cliDocumentationSelector, []string{"./internal/contract"})
	assert.Equal(t, []string{"TestCLIUsabilityDocumentationDrift", "TestReadmeRelativeLinksResolve", "TestS6DocumentationDrift"}, selectedDocumentation)

	criterionPrimaryOwners := map[string]string{
		"cli.AC-1": "cli_e2e", "cli.AC-2": "cli_e2e", "cli.AC-3": "cli_e2e",
		"cli.AC-4": "cli_e2e", "cli.AC-5": "cli_e2e", "cli.AC-6": "cli_unit",
		"cli.AC-7": "cli_unit", "cli.AC-8": "cli_source", "cli.AC-9": "cli_documentation",
	}
	require.Len(t, criterionPrimaryOwners, 9)
	for index := 1; index <= 9; index++ {
		ownerID, ok := criterionPrimaryOwners["cli.AC-"+strconv.Itoa(index)]
		require.True(t, ok)
		_, ok = ids[ownerID]
		require.True(t, ok, ownerID)
	}
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

func selectedTests(t *testing.T, selector string, packages []string) []string {
	t.Helper()
	matcher := regexp.MustCompile(selector)
	selected := make([]string, 0)
	for _, name := range discoverTests(t, repositoryRoot(t), packages) {
		if matcher.MatchString(name) {
			selected = append(selected, name)
		}
	}
	sort.Strings(selected)
	return selected
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
