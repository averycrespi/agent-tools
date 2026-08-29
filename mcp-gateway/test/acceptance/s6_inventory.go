package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

const S6InventoryVersion = 1

type S6LeafCategory string

const (
	S6LeafNPM         S6LeafCategory = "npm"
	S6LeafGo          S6LeafCategory = "go"
	S6LeafIntegration S6LeafCategory = "integration"
	S6LeafSecurity    S6LeafCategory = "security"
	S6LeafBrowser     S6LeafCategory = "browser"
	S6LeafE2E         S6LeafCategory = "e2e"
	S6LeafExternal    S6LeafCategory = "external"
)

type S6PlannedLeaf struct {
	ID              string         `json:"id"`
	Category        S6LeafCategory `json:"category"`
	CWD             string         `json:"cwd"`
	Argv            []string       `json:"argv"`
	ExpectedMatches int            `json:"expected_matches"`
	GatewayStarts   int            `json:"gateway_starts"`
	ChromiumStarts  int            `json:"chromium_starts"`
	Artifacts       []string       `json:"artifacts"`
	Cleanup         []string       `json:"cleanup"`
}

type S6PlannedOwner struct {
	ID                 string          `json:"id"`
	Layer              string          `json:"layer"`
	Intent             string          `json:"intent"`
	Criteria           []string        `json:"criteria"`
	Capabilities       []string        `json:"capabilities"`
	Status             string          `json:"status"`
	Budget             time.Duration   `json:"budget"`
	Blocking           bool            `json:"blocking"`
	NestedDependencies []string        `json:"nested_dependencies"`
	Leaves             []S6PlannedLeaf `json:"leaves"`
	DefinitionHash     string          `json:"definition_hash"`
}

type S6Baseline struct {
	ID       string        `json:"id"`
	Duration time.Duration `json:"duration"`
	Source   string        `json:"source"`
}

var s6TaskCategories = map[string][]S6LeafCategory{
	"T1": {S6LeafGo}, "T2": {S6LeafNPM, S6LeafNPM, S6LeafBrowser}, "T3": {S6LeafGo}, "T4": {S6LeafGo}, "T5": {S6LeafBrowser},
	"T6": {S6LeafGo}, "T7": {S6LeafIntegration}, "T8": {S6LeafGo}, "T9": {S6LeafE2E}, "T10": {S6LeafGo, S6LeafBrowser},
	"T11": {S6LeafBrowser}, "T12": {S6LeafBrowser}, "T13": {S6LeafBrowser}, "T14": {S6LeafBrowser}, "T15": {S6LeafBrowser},
	"T16": {S6LeafGo}, "T17": {S6LeafGo}, "T18": {S6LeafGo}, "T19": {S6LeafE2E},
	"T20": {S6LeafBrowser}, "T21": {S6LeafBrowser}, "T22": {S6LeafBrowser}, "T23": {S6LeafBrowser}, "T24": {S6LeafBrowser},
	"T25": {S6LeafBrowser}, "T26": {S6LeafBrowser}, "T27": {S6LeafBrowser}, "T28": {S6LeafBrowser}, "T29": {S6LeafBrowser},
	"T30": {S6LeafBrowser}, "T31": {S6LeafBrowser}, "T32": {S6LeafBrowser}, "T33": {S6LeafBrowser}, "T34": {S6LeafBrowser},
	"T35": {S6LeafBrowser}, "T36": {S6LeafBrowser}, "T37": {S6LeafBrowser},
	"T38": {S6LeafE2E}, "T39": {S6LeafE2E}, "T40": {S6LeafE2E}, "T41": {S6LeafE2E}, "T42": {S6LeafE2E}, "T43": {S6LeafE2E},
	"T44": {S6LeafE2E}, "T45": {S6LeafE2E}, "T46": {S6LeafE2E}, "T47": {S6LeafE2E}, "T48": {S6LeafE2E}, "T49": {S6LeafE2E},
	"T50": {S6LeafBrowser}, "T51": {S6LeafBrowser}, "T52": {S6LeafSecurity, S6LeafBrowser}, "T53": {S6LeafGo},
	"T54": {S6LeafNPM, S6LeafGo}, "T55": {S6LeafGo, S6LeafIntegration}, "T56": {S6LeafNPM, S6LeafGo}, "T57": {S6LeafGo}, "T58": {S6LeafExternal},
}

var s6MilestoneCategories = map[string][]S6LeafCategory{
	"M1": {S6LeafGo, S6LeafBrowser}, "M2": {S6LeafIntegration, S6LeafE2E}, "M3": {S6LeafBrowser},
	"M4": {S6LeafGo, S6LeafE2E}, "M5": {S6LeafBrowser}, "M6": {S6LeafBrowser}, "M7": {S6LeafBrowser}, "M8": {S6LeafBrowser},
	"M9": {S6LeafE2E}, "M10": {S6LeafGo, S6LeafE2E}, "M11": {S6LeafGo, S6LeafBrowser},
}

var s6Selectors = map[string][]string{
	"T2": {"ui:typecheck", "ui:verify-generated", "TestBrowserCoordinator"}, "T3": {"Test(AdminSessionBootstrapContract|AdminSessionBootstrapLifecycle|AdminSessionBootstrapAPI|AdminSessionBootstrapBoundary)"}, "T4": {"Test(BrowserEventPostContract|BrowserEventPostAPI|BrowserEventPostSubscription|BrowserEventPostBoundary)"}, "T5": {"TestBrowserProtocol"},
	"T6": {"TestInvocationReadProjectionContract"}, "T7": {"TestInvocationRepositoryReadsIntegration"}, "T8": {"Test(InvocationReadRootOwnership|InvocationReadAPI|InvocationReadComposition)"}, "T9": {"TestE2EInvocationReadPrivacy"},
	"T10": {"TestStaticDelivery", "TestBrowserFragmentStorage"}, "T11": {"TestBrowserAuthenticationEpoch"}, "T12": {"TestBrowserReadGeneration"},
	"T13": {"TestBrowserMutationState"}, "T14": {"TestBrowserShellPrimitives"}, "T15": {"TestBrowserSecretSinks"},
	"T16": {"TestControlTransport"}, "T17": {"Test(CLISharedContract|ControlClientSharedContract)"}, "T18": {"Test(CLISensitiveSinks|AdminCredentialSensitiveSinks|ControlClientSensitiveSinks|CLISecretSinkBoundaries)"}, "T19": {"TestCLIStatusInvocations"},
	"T20": {"TestBrowserOverview"}, "T21": {"TestBrowserInvocations"}, "T22": {"TestBrowserSystemStatus"}, "T23": {"TestBrowserServerCatalogReads"},
	"T24": {"TestBrowserServerCreateUpdate"}, "T25": {"TestBrowserServerOperations"}, "T26": {"TestBrowserServerCredentials"},
	"T27": {"TestBrowserAuthFlows"}, "T28": {"TestBrowserServerDisconnectDelete"}, "T29": {"TestBrowserPrincipals"},
	"T30": {"TestBrowserPrincipalCredentials"}, "T31": {"TestBrowserGrantReadsCreate"}, "T32": {"TestBrowserGrantCorrection"},
	"T33": {"TestBrowserRequestReads"}, "T34": {"TestBrowserRequestAdjudication"}, "T35": {"TestBrowserAdminCredentials"},
	"T36": {"TestBrowserBackups"}, "T37": {"TestBrowserCapabilityAudit"}, "T38": {"TestCLIServerCatalogReads"},
	"T39": {"TestCLIServerCreateUpdate"}, "T40": {"TestCLIServerDelete"}, "T41": {"TestCLIServerOperations"},
	"T42": {"TestCLIServerCredentials"}, "T43": {"TestCLIAuthFlows"}, "T44": {"TestCLIAdminCredentials"}, "T45": {"TestCLIBackups"},
	"T46": {"TestCLIPrincipals"}, "T47": {"TestCLIPrincipalCredentials"}, "T48": {"TestCLIGrants"}, "T49": {"TestCLIGrantRequests"},
	"T50": {"TestS6BrowserAccessibilityChanged"}, "T51": {"TestS6BrowserVisualChanged"}, "T52": {"TestS6SecurityCanaries", "TestS6BrowserPrivacyCanary"},
	"T53": {"TestS6ExternalEvidenceContract"}, "T54": {"ui:verify-supply-chain", "TestStaticSupplyChain"},
	"T55": {"Test(FoundationRoutesAndMechanicsAreExact|ServerRoutesAndMechanicsAreExact|AuthorizationRoutesAndMechanicsAreExact|GrantRequestRoutesProblemsAndLimitsAreExact|GrantRequestResourceShapesETagsMechanicsAndStatusAreExact|InvocationReadProjectionContract|GrantRequestStrictQueriesBodiesPreconditionsAndProblems|StaticDelivery|BrowserEventPostContract|BrowserEventPostAPI|BrowserEventPostSubscription|BrowserEventPostBoundary|ProductionSourceOwnershipGuards|ProductionCompositionAuthorityMatrixUsesOneGraphAndActualOwners|AdmissionInputCannotCarryForbiddenPayloads|SinkFailureNeverActivatesUndisclosedAuthority|EventsPreserveIDFreeOrderOverflowLossAndReconnect|CLISharedContract|ControlClientSharedContract|RequestSchemaMigrationRollsBackAtomically)", "TestRestoreAcceptedSchemaLineages"}, "T56": {"format:check", "Test(CLIDocumentationDrift|DocumentationContractDrift)"}, "T57": {"TestS6AcceptanceProfile"},
	"M1": {"Test(SessionEventContractIntegration|AdminSessionShutdownInvalidatesBrowserSession|StaticShellSecurityHeaders|EventHubClosesWithBrowserSessionAndShutdown|BoundaryRoutesSessionAndEventAuthorities)", "TestBrowserSessionLifecycleCanary"}, "M2": {"Test(InvocationReadPaginationIntegration|InvocationReadCompositionIntegration)", "TestInvocationReadOnlyAPICanary"}, "M3": {"TestBrowserPriorSessionResponseIsolationCanary"},
	"M4": {"TestCLIPrivateAuthorityBoundary", "TestCLIOnlineOfflineOutputCanary"}, "M5": {"TestBrowserOverviewInvocationSystemCanary"}, "M6": {"TestBrowserServerManagementCanary"},
	"M7": {"TestBrowserAccessManagementReadCanary"}, "M8": {"TestBrowserSystemAdministrationCanary"}, "M9": {"TestCLIServerLifecycleCanary"}, "M10": {"Test(CLISharedContract|ControlClientSharedContract)", "TestCLIAuthorityLifecycleCanary"},
	"M11": {"TestS6M11Gate", "TestS6BrowserM11Canary"},
}

var s6Packages = map[string][]string{
	"T3": {"./internal/contract", "./internal/admin", "./internal/httpboundary", "./internal/api"}, "T4": {"./internal/contract", "./internal/httpboundary", "./internal/api", "./internal/events"},
	"T6": {"./internal/contract"}, "T7": {"./internal/invocation"}, "T8": {"./internal/contract", "./internal/api", "./internal/composition", "./cmd/mcp-gateway"},
	"T10": {"./internal/api"}, "T16": {"./internal/controlclient"}, "T17": {"./internal/controlclient", "./cmd/mcp-gateway"},
	"T18": {"./internal/controlclient", "./internal/admin", "./internal/paths", "./cmd/mcp-gateway"}, "T53": {"./test/acceptance"},
	"T54": {"./internal/api"}, "T55": {"./internal/contract", "./internal/api", "./internal/composition", "./internal/invocation", "./internal/admin", "./internal/events", "./internal/controlclient", "./cmd/mcp-gateway", "./internal/storage", "./internal/backup"},
	"T56": {"./internal/contract", "./cmd/mcp-gateway"}, "T57": {"./test/acceptance"},
	"M1": {"./internal/contract", "./internal/admin", "./internal/httpboundary", "./internal/api", "./internal/events"},
	"M2": {"./internal/invocation", "./internal/composition"}, "M4": {"./internal/controlclient", "./cmd/mcp-gateway"},
	"M10": {"./internal/controlclient", "./cmd/mcp-gateway"}, "M11": {"./internal/contract", "./test/acceptance"},
}

var S6FinalCheckIDs = []string{
	"format", "gateway_verify", "frontend", "go_unit", "integration_compat", "browser_workflows", "browser_visual", "browser_a11y",
	"browser_cross", "cli_e2e", "security_privacy", "go_vulnerability", "frontend_vulnerability", "native", "other_tools", "diff",
}

var s6FinalArgv = map[string][]string{
	"format":                 {"npm", "run", "format:check"},
	"gateway_verify":         {"make", "-C", "mcp-gateway", "verify"},
	"frontend":               {"make", "-C", "mcp-gateway", "test-frontend-s6"},
	"go_unit":                {"make", "-C", "mcp-gateway", "test-unit-s6"},
	"integration_compat":     {"make", "-C", "mcp-gateway", "test-integration-s6"},
	"browser_workflows":      {"make", "-C", "mcp-gateway", "test-browser-s6-workflows"},
	"browser_visual":         {"make", "-C", "mcp-gateway", "test-browser-s6-visual"},
	"browser_a11y":           {"make", "-C", "mcp-gateway", "test-browser-s6-a11y"},
	"browser_cross":          {"make", "-C", "mcp-gateway", "test-browser-s6-cross"},
	"cli_e2e":                {"make", "-C", "mcp-gateway", "test-cli-e2e-s6"},
	"security_privacy":       {"make", "-C", "mcp-gateway", "test-security-s6"},
	"go_vulnerability":       {"go", "-C", "mcp-gateway", "tool", "govulncheck", "./..."},
	"frontend_vulnerability": {"npm", "audit", "--audit-level=high"},
	"native":                 {"make", "-C", "mcp-gateway", "test-keyring-native"},
	"other_tools":            {"make", "check-other-tools"},
	"diff":                   {"git", "diff", "--check", "63618c0c7bac399c872d3b069ddd2e2b923cd12f..HEAD", "--"},
}

var s6FinalBudgets = map[string]time.Duration{
	"format": 10 * time.Second, "gateway_verify": 10 * time.Second, "frontend": 40 * time.Second,
	"go_unit": 110 * time.Second, "integration_compat": 60 * time.Second, "browser_workflows": 2 * time.Minute,
	"browser_visual": 60 * time.Second, "browser_a11y": 45 * time.Second, "browser_cross": 45 * time.Second,
	"cli_e2e": 90 * time.Second, "security_privacy": 30 * time.Second, "go_vulnerability": 15 * time.Second,
	"frontend_vulnerability": 15 * time.Second, "native": 10 * time.Second, "other_tools": 15 * time.Second, "diff": 5 * time.Second,
}

var S6Baselines = []S6Baseline{
	{ID: "unit", Duration: 85665 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "integration", Duration: 24973 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "security", Duration: 1704 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "stress", Duration: 40648 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "e2e", Duration: 45335 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "gateway_verify", Duration: 3676 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "format", Duration: 791 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "other_tools", Duration: 5486 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "native", Duration: 286 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "go_vulnerability", Duration: 2617 * time.Millisecond, Source: "S5 accepted profile"},
	{ID: "profile", Duration: 211469 * time.Millisecond, Source: "S5 accepted profile"},
}

func S6PlannedInventory() []S6PlannedOwner {
	owners := make([]S6PlannedOwner, 0, 69)
	for task := 1; task <= 58; task++ {
		id := fmt.Sprintf("T%d", task)
		owner := S6PlannedOwner{ID: id, Layer: "task", Intent: "Prove the bounded " + id + " implementation seam", Status: "planned", Budget: 2 * time.Minute, Blocking: true}
		switch id {
		case "T1":
			owner.Status = "executable"
			owner.Intent = "Validate the frozen S6 manifests and command inventory"
		case "T2", "T3", "T4", "T5", "T6", "T7", "T8", "T9", "T10", "T11", "T12", "T13", "T14", "T15", "T16", "T17", "T18", "T19", "T20", "T21", "T22", "T23", "T24", "T25", "T26", "T27", "T28", "T29", "T30", "T31", "T32", "T33", "T34", "T35", "T36", "T37", "T38", "T39", "T40", "T41", "T42", "T43", "T44", "T45", "T46", "T47", "T48", "T49", "T50", "T51", "T52", "T53", "T54", "T55", "T56", "T57":
			owner.Status = "executable"
		}
		owner.Leaves = s6Leaves(id, s6TaskCategories[id])
		owner.DefinitionHash = s6OwnerHash(owner)
		owners = append(owners, owner)
	}
	for milestone := 1; milestone <= 11; milestone++ {
		id := fmt.Sprintf("M%d", milestone)
		owner := S6PlannedOwner{ID: id, Layer: "milestone", Intent: "Run the disjoint " + id + " integration canary", Status: "planned", Budget: 5 * time.Minute, Blocking: true, Leaves: s6Leaves(id, s6MilestoneCategories[id])}
		if id == "M1" || id == "M2" || id == "M3" || id == "M4" || id == "M5" || id == "M6" || id == "M7" || id == "M8" || id == "M9" || id == "M10" || id == "M11" {
			owner.Status = "executable"
		}
		owner.DefinitionHash = s6OwnerHash(owner)
		owners = append(owners, owner)
	}
	return owners
}

func S6FinalInventory() []S6PlannedOwner {
	owners := make([]S6PlannedOwner, 0, len(S6FinalCheckIDs))
	for _, id := range S6FinalCheckIDs {
		owner := S6PlannedOwner{ID: id, Layer: "final", Intent: "Run the sole final " + id + " evidence owner", Status: "executable", Budget: s6FinalBudgets[id], Blocking: true}
		owner.Leaves = []S6PlannedLeaf{{ID: "final." + id, Category: finalCategory(id), CWD: ".", Argv: append([]string(nil), s6FinalArgv[id]...), ExpectedMatches: 1, Artifacts: []string{"acceptance report check metadata"}, Cleanup: []string{"processes", "listeners", "temporary roots"}}}
		owner.DefinitionHash = s6OwnerHash(owner)
		owners = append(owners, owner)
	}
	return owners
}

func S6Owner(id string) (S6PlannedOwner, bool) {
	for _, owner := range append(S6PlannedInventory(), S6FinalInventory()...) {
		if owner.ID == id {
			return owner, true
		}
	}
	return S6PlannedOwner{}, false
}

func ValidateS6OwnerForExecution(id string) error {
	owner, ok := S6Owner(id)
	if !ok {
		return fmt.Errorf("unknown S6 owner %q", id)
	}
	if owner.Status != "executable" {
		return fmt.Errorf("S6 owner %s is planned, not executable", id)
	}
	return nil
}

func RunS6Owner(ctx context.Context, root, id string, executor Executor) error {
	if err := ValidateS6OwnerForExecution(id); err != nil {
		return err
	}
	owner, _ := S6Owner(id)
	ownerContext, cancel := context.WithTimeout(ctx, owner.Budget)
	defer cancel()
	for _, leaf := range owner.Leaves {
		if len(leaf.Argv) == 0 {
			return fmt.Errorf("S6 owner %s leaf %s has no command", id, leaf.ID)
		}
		command := Command{CheckName: leaf.ID, Name: leaf.Argv[0], Arguments: append([]string(nil), leaf.Argv[1:]...), Timeout: owner.Budget}
		if _, err := executor.Run(ownerContext, root, command); err != nil {
			return fmt.Errorf("S6 owner %s leaf %s: %w", id, leaf.ID, err)
		}
	}
	return nil
}

func s6Leaves(owner string, categories []S6LeafCategory) []S6PlannedLeaf {
	leaves := make([]S6PlannedLeaf, 0, len(categories))
	for index, category := range categories {
		leaf := S6PlannedLeaf{ID: fmt.Sprintf("%s.%d", owner, index+1), Category: category, CWD: ".", ExpectedMatches: 1, Cleanup: []string{"processes", "listeners", "temporary roots"}}
		selector := ""
		if selectors := s6Selectors[owner]; index < len(selectors) {
			selector = selectors[index]
		}
		switch category {
		case S6LeafNPM:
			leaf.Argv = []string{"npm", "run", selector}
		case S6LeafExternal:
			leaf.Argv = []string{"make", "-C", "mcp-gateway", "qualify-external-s6"}
		default:
			tags := ""
			switch category {
			case S6LeafIntegration:
				tags = "integration"
			case S6LeafSecurity:
				tags = "security"
			case S6LeafBrowser:
				tags = "e2e,browser"
			case S6LeafE2E:
				tags = "e2e"
			}
			leaf.Argv = []string{"go", "-C", "mcp-gateway", "test", "-race", "-count=1"}
			if tags != "" {
				leaf.Argv = append(leaf.Argv, "-tags="+tags)
			}
			leaf.Argv = append(leaf.Argv, "-timeout=2m", "-run", "^"+selector+"$")
			packages := s6Packages[owner]
			switch category {
			case S6LeafBrowser, S6LeafE2E:
				packages = []string{"./test/e2e"}
			case S6LeafSecurity:
				packages = []string{"./test/security"}
			}
			leaf.Argv = append(leaf.Argv, packages...)
		}
		switch category {
		case S6LeafBrowser:
			leaf.GatewayStarts = 1
			leaf.ChromiumStarts = 1
		case S6LeafE2E:
			leaf.GatewayStarts = 1
		}
		if owner == "T1" {
			leaf.Argv = []string{"go", "-C", "mcp-gateway", "test", "-race", "-count=1", "-timeout=2m", "-run", "^Test(LegacyControlPlaneManifest|S6(Inventory|PlannedCommand|Capability|Documentation))$", "./internal/contract", "./test/acceptance"}
			leaf.ExpectedMatches = 5
		}
		if owner == "T2" && index < 2 {
			leaf.Artifacts = []string{"two normalized temporary frontend builds"}
		}
		if owner == "T5" {
			leaf.GatewayStarts = 2
		}
		if owner == "T7" {
			leaf.Artifacts = []string{"one 4,096/N/N+1 retention fixture"}
		}
		if owner == "T51" {
			leaf.Artifacts = []string{"two representative screenshots"}
		}
		if owner == "T55" {
			leaf.ExpectedMatches = []int{19, 1}[index]
		}
		if owner == "T56" && category == S6LeafGo {
			leaf.ExpectedMatches = 2
		}
		leaves = append(leaves, leaf)
	}
	return leaves
}

func s6OwnerHash(owner S6PlannedOwner) string {
	owner.DefinitionHash = ""
	encoded, err := json.Marshal(owner)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func finalCategory(id string) S6LeafCategory {
	switch id {
	case "frontend", "frontend_vulnerability", "format":
		return S6LeafNPM
	case "browser_workflows", "browser_visual", "browser_a11y", "browser_cross":
		return S6LeafBrowser
	case "cli_e2e":
		return S6LeafE2E
	case "security_privacy":
		return S6LeafSecurity
	case "integration_compat":
		return S6LeafIntegration
	default:
		return S6LeafGo
	}
}

func S6InventoryDefinitionHash() string {
	owners := append(S6PlannedInventory(), S6FinalInventory()...)
	sort.Slice(owners, func(i, j int) bool { return owners[i].ID < owners[j].ID })
	encoded, err := json.Marshal(owners)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
