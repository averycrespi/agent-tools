package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

const releaseProfileBudget = 15 * time.Minute

type finalReleaseCheckSpec struct {
	ID            string
	Argv          []string
	Timeout       time.Duration
	Budget        time.Duration
	Repeats       int
	GatewayStarts int
	BrowserStarts int
	Native        bool
}

func finalReleaseProfile(root string) (releaseProfileDefinition, error) {
	external, err := releaseExternalEvidenceDefinitions(root)
	if err != nil {
		return releaseProfileDefinition{}, err
	}
	checks, err := finalReleaseChecks()
	if err != nil {
		return releaseProfileDefinition{}, err
	}
	definitionFiles, err := finalReleaseDefinitionFiles(root)
	if err != nil {
		return releaseProfileDefinition{}, err
	}
	definition := releaseProfileDefinition{
		Profile: releaseProfile, BudgetMillis: releaseProfileBudget.Milliseconds(),
		Coverage: releaseCoverage{ProductBehaviors: canonicalReleaseProductBehaviors(), CleanupCriteria: canonicalReleaseCleanupCriteria()},
		Checks:   checks, ExternalEvidence: external, DefinitionFiles: definitionFiles,
	}
	if err := validateFinalReleaseProfile(root, definition); err != nil {
		return releaseProfileDefinition{}, err
	}
	return definition, nil
}

func finalReleaseChecks() ([]releaseCheckDefinition, error) {
	specs, err := finalReleaseCheckSpecs()
	if err != nil {
		return nil, err
	}
	productOwners := releaseProductBehaviorOwners()
	cleanupOwners := map[string]string{
		"cleanup.AC-1": "test-harness", "cleanup.AC-2": "repository-verify", "cleanup.AC-3": "repository-verify",
		"cleanup.AC-4": "repository-diff", "cleanup.AC-5": "repository-verify", "cleanup.AC-6": "test-security",
		"cleanup.AC-7": "test-browser-accessibility", "cleanup.AC-8": "test-stress", "cleanup.AC-9": "test-integration", "cleanup.AC-10": "test-harness",
	}
	checks := make([]releaseCheckDefinition, len(specs))
	byID := make(map[string]int, len(specs))
	for index, spec := range specs {
		byID[spec.ID] = index
		checks[index] = releaseCheckDefinition{
			ID: spec.ID, Argv: append([]string(nil), spec.Argv...), TimeoutMillis: spec.Timeout.Milliseconds(), BudgetMillis: spec.Budget.Milliseconds(),
			Repeats: spec.Repeats, ExpectedGatewayStarts: spec.GatewayStarts, ExpectedBrowserStarts: spec.BrowserStarts,
			Artifacts: []string{"artifacts/" + spec.ID + ".json"}, CleanupRequirements: []string{"processes", "listeners", "temporary roots"}, Native: spec.Native,
			Coverage: releaseCoverage{ProductBehaviors: []string{}, CleanupCriteria: []string{}},
		}
	}
	for _, id := range canonicalReleaseProductBehaviors() {
		owner, ok := productOwners[id]
		if !ok {
			return nil, fmt.Errorf("release behavior %s has no final owner", id)
		}
		index, ok := byID[owner]
		if !ok {
			return nil, fmt.Errorf("release behavior %s references unknown final owner %s", id, owner)
		}
		checks[index].Coverage.ProductBehaviors = append(checks[index].Coverage.ProductBehaviors, id)
	}
	for _, id := range canonicalReleaseCleanupCriteria() {
		index, ok := byID[cleanupOwners[id]]
		if !ok {
			return nil, fmt.Errorf("release cleanup criterion %s has no final owner", id)
		}
		checks[index].Coverage.CleanupCriteria = append(checks[index].Coverage.CleanupCriteria, id)
	}
	return checks, nil
}

func finalReleaseCheckSpecs() ([]finalReleaseCheckSpec, error) {
	dag := purposeEvidenceDAG()
	if err := validatePurposeEvidenceDAG(dag); err != nil {
		return nil, err
	}
	fromPurpose := func(id string) (finalReleaseCheckSpec, error) {
		leaf, ok := dag.Leaves[id]
		if !ok {
			return finalReleaseCheckSpec{}, fmt.Errorf("unknown final purpose leaf %s", id)
		}
		command := dag.Commands[leaf.Command]
		argv := append([]string(nil), command.Argv...)
		if id == "test-stress" {
			argv = []string{"make", "-C", "agent-gateway", "AGENT_GATEWAY_STRESS_COUNT=20", "test-stress"}
		}
		if id == "test-keyring-native" {
			argv = []string{"agent-gateway/test/keyring-native.sh"}
		}
		return finalReleaseCheckSpec{ID: id, Argv: argv, Timeout: leaf.Timeout, Budget: leaf.Budget, Repeats: leaf.Repeats, GatewayStarts: leaf.GatewayStarts, BrowserStarts: leaf.BrowserStarts, Native: id == "test-keyring-native"}, nil
	}
	special := map[string]finalReleaseCheckSpec{
		"repository-format":      {ID: "repository-format", Argv: []string{"npm", "run", "format:check"}, Timeout: 30 * time.Second, Budget: 60 * time.Second, Repeats: 1},
		"repository-verify":      {ID: "repository-verify", Argv: []string{"make", "-C", "agent-gateway", "verify"}, Timeout: 2 * time.Minute, Budget: 3 * time.Minute, Repeats: 1},
		"go-vulnerability":       {ID: "go-vulnerability", Argv: []string{"go", "-C", "agent-gateway", "tool", "govulncheck", "./..."}, Timeout: 60 * time.Second, Budget: 90 * time.Second, Repeats: 1},
		"repository-other-tools": {ID: "repository-other-tools", Argv: []string{"make", "check-other-tools"}, Timeout: 2 * time.Minute, Budget: 3 * time.Minute, Repeats: 1},
		"repository-diff":        {ID: "repository-diff", Argv: []string{"git", "diff", "--check"}, Timeout: 10 * time.Second, Budget: 30 * time.Second, Repeats: 1},
	}
	order := finalReleaseOrder()
	specs := make([]finalReleaseCheckSpec, 0, len(order))
	for _, id := range order {
		if spec, ok := special[id]; ok {
			specs = append(specs, spec)
			continue
		}
		spec, err := fromPurpose(id)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func finalReleaseOrder() []string {
	return []string{
		"repository-format", "repository-verify", "test-unit", "test-integration", "test-harness", "test-serve-demo", "test-e2e", "test-security", "test-stress", "test-keyring-native",
		"test-browser-workflows", "test-browser-privacy", "test-browser-visual", "test-browser-accessibility",
		"test-frontend-development-node", "test-frontend-development-browser", "frontend-typecheck", "frontend-verify-supply-chain", "go-vulnerability", "frontend-audit", "repository-other-tools", "repository-diff",
	}
}

func canonicalReleaseProductBehaviors() []string {
	ids := make([]string, 0, 232)
	for _, row := range contract.ProductBehaviorManifest() {
		ids = append(ids, row.ID)
	}
	for _, row := range contract.SecurityBehaviorManifest() {
		ids = append(ids, row.ID)
	}
	for _, row := range contract.PredecessorBehaviorManifest() {
		ids = append(ids, row.ID)
	}
	for _, row := range contract.DocumentationBehaviorManifest() {
		ids = append(ids, row.ID)
	}
	for _, row := range contract.EvidenceTierManifest() {
		if !row.Optional {
			ids = append(ids, row.ID)
		}
	}
	return ids
}

func canonicalReleaseCleanupCriteria() []string {
	criteria := make([]string, 10)
	for index := range criteria {
		criteria[index] = fmt.Sprintf("cleanup.AC-%d", index+1)
	}
	return criteria
}

func releaseProductBehaviorOwners() map[string]string {
	tierOwners := map[string]string{
		"tier.repository.format": "repository-format", "tier.repository.verify": "repository-verify", "tier.frontend.static": "frontend-typecheck",
		"tier.unit.contract": "test-unit", "tier.integration.compatibility": "test-integration", "tier.harness.selftests": "test-harness", "tier.harness.temporary": "test-serve-demo", "tier.browser.workflows": "test-browser-workflows",
		"tier.browser.visual": "test-browser-visual", "tier.browser.accessibility": "test-browser-accessibility", "tier.browser.cross": "test-browser-cross",
		"tier.e2e.complete": "test-e2e", "tier.security.privacy": "test-security", "tier.supply_chain.go": "go-vulnerability",
		"tier.supply_chain.frontend": "frontend-audit", "tier.native.keyring": "test-keyring-native", "tier.repository.other_tools": "repository-other-tools", "tier.repository.diff": "repository-diff",
	}
	owners := make(map[string]string, 232)
	for _, row := range contract.ProductBehaviorManifest() {
		owners[row.ID] = tierOwners[row.EvidenceOwner]
	}
	for _, row := range contract.SecurityBehaviorManifest() {
		owners[row.ID] = tierOwners[row.EvidenceOwner]
	}
	for _, row := range contract.PredecessorBehaviorManifest() {
		owners[row.ID] = tierOwners[row.EvidenceOwner]
	}
	for _, row := range contract.DocumentationBehaviorManifest() {
		owners[row.ID] = tierOwners[row.EvidenceOwner]
	}
	for _, row := range contract.EvidenceTierManifest() {
		owners[row.ID] = tierOwners[row.ID]
	}
	overrides := map[string]string{
		"product.grant_request.conflict_and_uncertainty": "test-stress", "product.grant_request.approval_narrowing": "test-stress",
		"product.invocation.page_coherence": "test-stress", "product.client_refresh.no_unsafe_replay": "test-stress", "product.invocation.missing_terminal_unknown": "test-stress",
		"frontend.proxy_boundary": "test-frontend-development-node", "frontend.style_live_reload": "test-frontend-development-browser",
		"frontend.module_live_reload": "test-frontend-development-browser", "frontend.control_plane": "test-frontend-development-browser",
		"frontend.streaming_uncertainty": "test-frontend-development-browser", "frontend.privacy": "test-browser-privacy",
		"product.frontend.static_supply_chain": "frontend-verify-supply-chain", "frontend.production_separation": "frontend-verify-supply-chain",
		"security.frontend.generated_assets": "frontend-verify-supply-chain",
	}
	for id, owner := range overrides {
		owners[id] = owner
	}
	return owners
}

func finalReleaseDefinitionFiles(root string) ([]string, error) {
	dag := purposeEvidenceDAG()
	leaves, err := expandPurposeLeaves(dag, dag.FinalLeaves)
	if err != nil {
		return nil, err
	}
	commands, err := expandPurposeCommands(dag, leaves)
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	add := func(paths ...string) {
		for _, path := range paths {
			set[path] = struct{}{}
		}
	}
	for _, leaf := range leaves {
		add(leaf.DefinitionFiles...)
	}
	for _, command := range commands {
		add(command.DefinitionFiles...)
	}
	add(
		"Makefile", ".gitignore", ".prettierignore", "package.json", "package-lock.json", "agent-gateway/Makefile", "agent-gateway/go.mod", "agent-gateway/go.sum", "agent-gateway/.golangci.yml",
		"agent-gateway/internal/contract/product_behavior_manifest.go", "agent-gateway/internal/contract/documentation_ownership.go", "agent-gateway/internal/contract/control_plane_capabilities.go",
		"agent-gateway/test/acceptance/acceptance.go", "agent-gateway/test/acceptance/cmd/main.go", "agent-gateway/test/acceptance/purpose_dag.go",
		"agent-gateway/test/acceptance/release_profile.go", "agent-gateway/test/acceptance/release_report.go", "agent-gateway/test/acceptance/release_runner.go",
		"agent-gateway/test/acceptance/suite_selection.go", "agent-gateway/test/keyringnative/result.go", "agent-gateway/test/keyringnative/result.schema.json", "agent-gateway/test/keyringnative/cmd/main.go",
		"agent-gateway/test/acceptance/release_report.schema.json", "agent-gateway/test/acceptance/release_external_evidence.go",
		"agent-gateway/test/acceptance/release_external_evidence.schema.json", "agent-gateway/web/environments.json",
		"agent-gateway/internal/testutil/development_environment.go", "agent-gateway/internal/testutil/cleanup_ledger.go", "agent-gateway/internal/testutil/cleanup_ledger_darwin.go", "agent-gateway/internal/testutil/cleanup_ledger_linux.go",
		"agent-gateway/internal/testutil/cleanup_ledger_other.go", "agent-gateway/internal/testutil/process_supervisor.go", "agent-gateway/internal/testutil/process_supervisor_other.go", "agent-gateway/internal/testutil/process_supervisor_unix.go",
	)
	for _, tool := range []string{"mcp-broker", "local-git-mcp", "http-broker", "typesafe-mcp"} {
		add(tool+"/Makefile", tool+"/.golangci.yml", tool+"/go.mod", tool+"/go.sum")
	}
	// Bind authored frontend modules and scenarios once, not in every browser leaf.
	// WalkDir never follows symlinks; reject them rather than hash an outside target.
	for _, directory := range []string{"agent-gateway/web/src", "agent-gateway/web/tests"} {
		if err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("symlink in release definitions: %s", path)
			}
			if entry.IsDir() {
				return nil
			}
			switch filepath.Ext(path) {
			case ".ts", ".tsx", ".css":
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				add(filepath.ToSlash(relative))
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	add("agent-gateway/internal/contract/server_states.go", "agent-gateway/internal/contract/authorization_states.go", "agent-gateway/internal/contract/invocation_projections.go", "agent-gateway/test/e2e/harness_deadline_browser_test.go")
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func validateFinalReleaseProfile(root string, definition releaseProfileDefinition) error {
	if err := validateReleaseProfileDefinition(definition); err != nil {
		return err
	}
	if definition.BudgetMillis != releaseProfileBudget.Milliseconds() || !reflect.DeepEqual(definition.Coverage.ProductBehaviors, canonicalReleaseProductBehaviors()) || !reflect.DeepEqual(definition.Coverage.CleanupCriteria, canonicalReleaseCleanupCriteria()) {
		return errors.New("final release profile does not have exact canonical coverage and budget")
	}
	expectedChecks, err := finalReleaseChecks()
	if err != nil {
		return err
	}
	if len(definition.Checks) != len(expectedChecks) {
		return errors.New("final release profile does not have the exact direct check set")
	}
	for index, expected := range expectedChecks {
		actual := definition.Checks[index]
		if actual.ID == "test-stress" && (actual.Repeats != 20 || !reflect.DeepEqual(actual.Argv, expected.Argv)) {
			return errors.New("final release stress multiplicity is not closed")
		}
		if !reflect.DeepEqual(actual.CleanupRequirements, []string{"processes", "listeners", "temporary roots"}) {
			return fmt.Errorf("final release check %s has incomplete cleanup requirements", actual.ID)
		}
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("final release check %s does not match its closed direct command and metadata", actual.ID)
		}
	}
	expectedDefinitions, err := finalReleaseDefinitionFiles(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(definition.DefinitionFiles, expectedDefinitions) {
		return errors.New("final release definition inventory is not exact and transitive")
	}
	expectedExternal := canonicalReleaseExternalEvidenceDefinitions()
	if !reflect.DeepEqual(definition.ExternalEvidence, expectedExternal) {
		return errors.New("final release external evidence definitions are not exact")
	}
	return nil
}

func cloneReleaseProfile(profile releaseProfileDefinition) releaseProfileDefinition {
	contents, _ := json.Marshal(profile)
	var clone releaseProfileDefinition
	_ = json.Unmarshal(contents, &clone)
	clone.DefinitionFiles = append([]string(nil), profile.DefinitionFiles...)
	return clone
}
