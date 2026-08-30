package acceptance

import (
	"fmt"
	"time"
)

type purposeEvidenceLeaf struct {
	ID              string
	BehaviorIDs     []string
	Command         string
	DefinitionFiles []string
	Timeout         time.Duration
	Budget          time.Duration
	Repeats         int
	GatewayStarts   int
	BrowserStarts   int
	Artifacts       []string
	Cleanup         []string
}

type purposeCommandNode struct {
	ID              string
	Argv            []string
	DefinitionFiles []string
	Dependencies    []string
}

type purposeEvidenceGraph struct {
	Leaves      map[string]purposeEvidenceLeaf
	Aggregates  map[string][]string
	Commands    map[string]purposeCommandNode
	FinalLeaves []string
}

var purposeStressTestIDs = []string{
	"TestApprovalCancellationPolicyLinearizationStress",
	"TestConcurrentSemanticDeduplicationStress",
	"TestLocalInvocationDrainCleanupStress",
	"TestLostLocalMutationResponseDeduplicatesExplicitRetryStress",
	"TestSyntheticSnapshotPaginationStress",
}

func purposeEvidenceDAG() purposeEvidenceGraph {
	const (
		makefile        = "mcp-gateway/Makefile"
		manifest        = "mcp-gateway/internal/contract/product_behavior_manifest.go"
		dagDefinition   = "mcp-gateway/test/acceptance/purpose_dag.go"
		packageManifest = "package.json"
		generatedScript = "mcp-gateway/web/scripts/verify-generated.mjs"
		supplyScript    = "mcp-gateway/web/scripts/verify-supply-chain.mjs"
	)
	commonDefinitions := []string{makefile, manifest, dagDefinition}
	defaultCleanup := []string{"processes", "listeners", "temporary roots"}
	leaf := func(id string, behaviorIDs []string, timeout, budget time.Duration, repeats, processStarts, browserStarts int, artifacts []string, extraDefinitions ...string) purposeEvidenceLeaf {
		definitions := append([]string(nil), commonDefinitions...)
		definitions = append(definitions, extraDefinitions...)
		return purposeEvidenceLeaf{
			ID: id, BehaviorIDs: append([]string(nil), behaviorIDs...), Command: "make." + id,
			DefinitionFiles: definitions, Timeout: timeout, Budget: budget, Repeats: repeats,
			GatewayStarts: processStarts, BrowserStarts: browserStarts,
			Artifacts: append([]string(nil), artifacts...), Cleanup: append([]string(nil), defaultCleanup...),
		}
	}

	leaves := map[string]purposeEvidenceLeaf{
		"test-unit":        leaf("test-unit", []string{"tier.unit.contract", "cli.help_and_errors"}, 90*time.Second, 2*time.Minute, 1, 0, 0, []string{"race-enabled Go test output"}),
		"test-integration": leaf("test-integration", []string{"tier.integration.compatibility", "cli.compatibility"}, 60*time.Second, 90*time.Second, 1, 0, 0, []string{"real SQLite and filesystem test output"}),
		"test-e2e":         leaf("test-e2e", []string{"tier.e2e.complete", "product.cli.command_tree", "product.cli.operator_parity"}, 2*time.Minute, 150*time.Second, 1, 66, 0, []string{"real-binary output", "process cleanup records"}),
		"test-security":    leaf("test-security", []string{"tier.security.privacy", "product.privacy.secret_boundaries", "security.tests.artifacts"}, 30*time.Second, 60*time.Second, 1, 0, 0, []string{"source and sink scan output"}),
		"test-stress": leaf("test-stress", []string{
			"product.grant_request.conflict_and_uncertainty", "product.grant_request.approval_narrowing", "product.invocation.page_coherence",
			"product.client_refresh.no_unsafe_replay", "product.invocation.missing_terminal_unknown",
		}, 2*time.Minute, 5*time.Minute, 20, 0, 0, []string{"five targeted race scenario results"}),
		"test-keyring-native": leaf("test-keyring-native", []string{"tier.native.keyring"}, 10*time.Second, 30*time.Second, 1, 0, 0, []string{"typed native keyring classification"}, "mcp-gateway/test/keyring-native.sh"),

		"test-browser-workflows":     leaf("test-browser-workflows", []string{"tier.browser.workflows", "product.interface.developer_first", "product.browser.authority_recovery"}, 2*time.Minute, 150*time.Second, 1, 33, 33, []string{"browser workflow output", "browser cleanup records"}, "mcp-gateway/test/e2e/harness_test.go"),
		"test-browser-privacy":       leaf("test-browser-privacy", []string{"security.browser.storage", "frontend.privacy"}, 30*time.Second, 45*time.Second, 1, 1, 1, []string{"secret canary scan", "browser cleanup records"}, "mcp-gateway/test/e2e/browser_secret_storage_privacy_test.go"),
		"test-browser-visual":        leaf("test-browser-visual", []string{"tier.browser.visual", "product.interface.developer_first"}, 60*time.Second, 75*time.Second, 1, 1, 1, []string{"deterministic visual matrix output"}, "mcp-gateway/test/e2e/browser_visual_responsive_test.go"),
		"test-browser-accessibility": leaf("test-browser-accessibility", []string{"tier.browser.accessibility", "product.browser.accessibility_responsive"}, 45*time.Second, 60*time.Second, 1, 1, 1, []string{"automated accessibility output"}, "mcp-gateway/test/e2e/browser_accessibility_test.go"),
		"test-browser-cross":         leaf("test-browser-cross", []string{"tier.browser.cross"}, 45*time.Second, 60*time.Second, 1, 1, 2, []string{"Firefox and WebKit compatibility output"}, "mcp-gateway/test/e2e/browser_cross_compatibility_test.go"),

		"test-frontend-development-node":    leaf("test-frontend-development-node", []string{"frontend.command_interface", "frontend.proxy_boundary"}, 30*time.Second, 45*time.Second, 1, 0, 0, []string{"Node development proxy matrix output"}, packageManifest, "mcp-gateway/web/dev-server.ts", "mcp-gateway/web/dev-proxy.ts", "mcp-gateway/web/tests/dev-server.test.ts", "mcp-gateway/web/tests/proxy-admission.test.ts"),
		"test-frontend-development-browser": leaf("test-frontend-development-browser", []string{"frontend.style_live_reload", "frontend.module_live_reload", "frontend.control_plane", "frontend.streaming_uncertainty"}, 2*time.Minute, 150*time.Second, 1, 2, 2, []string{"live-reload browser output", "development process cleanup records"}, "mcp-gateway/test/e2e/frontend_development_test.go", "mcp-gateway/test/e2e/frontend_control_plane_test.go"),
		"frontend-typecheck":                leaf("frontend-typecheck", []string{"tier.frontend.static", "frontend.command_interface"}, 30*time.Second, 45*time.Second, 1, 0, 0, []string{"TypeScript diagnostics"}, packageManifest, "mcp-gateway/web/tsconfig.json"),
		"frontend-build":                    leaf("frontend-build", []string{"product.frontend.static_supply_chain"}, 60*time.Second, 90*time.Second, 1, 0, 0, []string{"generated production asset allowlist"}, packageManifest, "mcp-gateway/web/vite.config.ts"),
		"frontend-verify-generated":         leaf("frontend-verify-generated", []string{"security.frontend.generated_assets"}, 60*time.Second, 90*time.Second, 1, 0, 0, []string{"deterministic generated-asset comparison"}, packageManifest, generatedScript, "mcp-gateway/web/vite.config.ts"),
		"frontend-verify-supply-chain":      leaf("frontend-verify-supply-chain", []string{"product.frontend.static_supply_chain", "frontend.production_separation", "security.frontend.generated_assets"}, 90*time.Second, 2*time.Minute, 1, 0, 0, []string{"dependency provenance", "generated-asset comparison", "static allowlist test output"}, packageManifest, generatedScript, supplyScript, "package-lock.json"),
		"frontend-audit":                    leaf("frontend-audit", []string{"tier.supply_chain.frontend"}, 60*time.Second, 60*time.Second, 1, 0, 0, []string{"unsuppressed npm vulnerability findings"}, packageManifest, "package-lock.json"),
	}

	commands := map[string]purposeCommandNode{}
	addCommand := func(id string, argv, definitions, dependencies []string) {
		commands[id] = purposeCommandNode{ID: id, Argv: append([]string(nil), argv...), DefinitionFiles: append([]string(nil), definitions...), Dependencies: append([]string(nil), dependencies...)}
	}
	addMake := func(target string, dependencies ...string) {
		addCommand("make."+target, []string{"make", "-C", "mcp-gateway", target}, []string{makefile}, dependencies)
	}

	addMake("test-unit", "go.unit")
	addCommand("go.unit", []string{"go", "test", "-race", "-count=1", "-timeout=90s", "./..."}, []string{makefile}, nil)
	addMake("test-integration", "go.integration")
	integrationSelector := "^(Test.*Integration|TestRequestSchemaMigrationUsesRealSQLite|TestRestoreAcceptedSchemaLineages|TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline|TestBusyBeyondDeadlineLatchesMutationAcrossRestart|TestRepositoryRetainsNewest4096ByMonotonicSequence|TestRepositoryRollsBackEvictionWhenInsertFails|TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss|TestSchemaSevenFixtureMigratesWithRealSQLite|TestPopulatedSchemaEightMigratesToNineWithRealSQLite|TestServerSchemaAndBackupContainNoSecretOrTransientRepresentation|TestServerSnapshotWatermarkExcludesLaterInsert|TestServicePublishesOneCompleteStaticGenerationAndSafeOperation)$"
	integrationPackages := []string{"./internal/storage", "./internal/backup", "./internal/servers", "./internal/servercredentials", "./internal/grantrequests", "./internal/authorization", "./internal/catalog", "./internal/invocation", "./internal/composition"}
	integrationArgv := []string{"go", "test", "-race", "-count=1", "-tags=integration", "-timeout=60s", "-run", integrationSelector}
	addCommand("go.integration", append(integrationArgv, integrationPackages...), []string{makefile}, nil)
	addMake("test-e2e", "go.e2e.complete")
	addCommand("go.e2e.complete", []string{"go", "test", "-race", "-count=1", "-tags=e2e", "-timeout=2m", "./test/e2e/..."}, []string{makefile}, nil)
	addMake("test-security", "go.security")
	addCommand("go.security", []string{"go", "test", "-race", "-count=1", "-tags=security", "-timeout=30s", "-run", "^(TestReleaseReportSecretSinkBoundaries|TestDurableSecretSinkBoundaries|TestSecurityEvidenceOwnerManifest|TestStaticSecretSinkClosure)$", "./test/security/...", "./test/acceptance"}, []string{makefile, "mcp-gateway/test/security/security_canaries_test.go", "mcp-gateway/test/acceptance/release_report_security_test.go"}, nil)
	addMake("test-stress", "go.stress.grant-requests", "go.stress.self-service", "go.stress.composition")
	addCommand("go.stress.grant-requests", []string{"go", "test", "-race", "-tags=stress", "-count=$(STRESS_COUNT)", "-timeout=2m", "-run", "^(TestConcurrentSemanticDeduplicationStress|TestApprovalCancellationPolicyLinearizationStress)$", "./internal/grantrequests"}, []string{makefile}, nil)
	addCommand("go.stress.self-service", []string{"go", "test", "-race", "-tags=stress", "-count=$(STRESS_COUNT)", "-timeout=90s", "-run", "^(TestSyntheticSnapshotPaginationStress|TestLostLocalMutationResponseDeduplicatesExplicitRetryStress)$", "./internal/selfservice"}, []string{makefile}, nil)
	addCommand("go.stress.composition", []string{"go", "test", "-race", "-tags=stress", "-count=$(STRESS_COUNT)", "-timeout=90s", "-run", "^TestLocalInvocationDrainCleanupStress$", "./internal/composition"}, []string{makefile}, nil)
	addMake("test-keyring-native", "shell.keyring-native")
	addCommand("shell.keyring-native", []string{"./test/keyring-native.sh"}, []string{makefile, "mcp-gateway/test/keyring-native.sh"}, nil)

	type browserCommand struct {
		selector string
		timeout  string
	}
	browserCommands := map[string]browserCommand{
		"test-browser-workflows":            {selector: "TestBrowser(Protocol|FragmentStorage|AuthenticationEpoch|ReadGeneration|MutationState|ShellPrimitives|SecretSinks|Overview|Invocations|SystemStatus|ServerCatalogReads|ServerCreateUpdate|ServerOperations|ServerCredentials|AuthFlows|ServerDisconnectDelete|Principals|PrincipalCredentials|GrantReadsCreate|GrantCorrection|RequestReads|RequestAdjudication|AdminCredentials|Backups|CapabilityAudit|SessionLifecycleCanary|PriorSessionResponseIsolationCanary|OverviewInvocationSystemCanary|ServerManagementCanary|AccessManagementReadCanary|SystemAdministrationCanary|VisualAccessibilityPrivacyCanary|Coordinator)", timeout: "2m"},
		"test-browser-privacy":              {selector: "TestBrowserSecretStoragePrivacy", timeout: "30s"},
		"test-browser-visual":               {selector: "TestBrowserVisualResponsiveMatrix", timeout: "60s"},
		"test-browser-accessibility":        {selector: "TestBrowserAccessibility", timeout: "45s"},
		"test-browser-cross":                {selector: "TestBrowserCrossCompatibility", timeout: "45s"},
		"test-frontend-development-browser": {selector: "TestFrontendDevelopment(LiveReload|ControlPlane)", timeout: "2m"},
	}
	for target, definition := range browserCommands {
		commandID := "go." + target
		addMake(target, commandID)
		addCommand(commandID, []string{"go", "test", "-race", "-count=1", "-tags=e2e,browser", "-timeout=" + definition.timeout, "-run", "^" + definition.selector + "$", "./test/e2e"}, []string{makefile}, nil)
	}

	addMake("test-frontend-development-node", "npm.ui.test-dev")
	addCommand("npm.ui.test-dev", []string{"npm", "--prefix", "..", "run", "ui:test-dev"}, []string{makefile, packageManifest}, []string{"node.test-dev"})
	addCommand("node.test-dev", []string{"node", "--test", "--test-timeout=30000"}, []string{packageManifest, "mcp-gateway/web/tests/dev-server.test.ts", "mcp-gateway/web/tests/proxy-admission.test.ts"}, nil)

	addMake("frontend-typecheck", "npm.ui.typecheck")
	addCommand("npm.ui.typecheck", []string{"npm", "--prefix", "..", "run", "ui:typecheck"}, []string{makefile, packageManifest}, []string{"tsc.frontend"})
	addCommand("tsc.frontend", []string{"tsc", "-p", "mcp-gateway/web/tsconfig.json", "--noEmit"}, []string{packageManifest, "mcp-gateway/web/tsconfig.json"}, nil)
	addMake("frontend-build", "npm.ui.build")
	addCommand("npm.ui.build", []string{"npm", "--prefix", "..", "run", "ui:build"}, []string{makefile, packageManifest}, []string{"vite.frontend-build"})
	addCommand("vite.frontend-build", []string{"vite", "build", "--config", "mcp-gateway/web/vite.config.ts"}, []string{packageManifest, "mcp-gateway/web/vite.config.ts"}, nil)
	addMake("frontend-verify-generated", "npm.ui.verify-generated")
	addCommand("npm.ui.verify-generated", []string{"npm", "--prefix", "..", "run", "ui:verify-generated"}, []string{makefile, packageManifest}, []string{"node.verify-generated"})
	addCommand("node.verify-generated", []string{"node", "mcp-gateway/web/scripts/verify-generated.mjs"}, []string{packageManifest, generatedScript, "mcp-gateway/web/vite.config.ts"}, nil)
	addMake("frontend-verify-supply-chain", "npm.ui.verify-supply-chain", "go.frontend-static-supply-chain")
	addCommand("npm.ui.verify-supply-chain", []string{"npm", "--prefix", "..", "run", "ui:verify-supply-chain"}, []string{makefile, packageManifest}, []string{"node.verify-supply-chain"})
	addCommand("node.verify-supply-chain", []string{"node", supplyScript}, []string{packageManifest, "package-lock.json", supplyScript}, []string{"node.verify-generated"})
	addCommand("go.frontend-static-supply-chain", []string{"go", "test", "-race", "-count=1", "-tags=frontend", "-timeout=30s", "-run", "^TestStaticSupplyChain$", "./internal/api"}, []string{makefile, "mcp-gateway/internal/api/static_supply_chain_frontend_test.go"}, nil)
	addMake("frontend-audit", "npm.ui.audit")
	addCommand("npm.ui.audit", []string{"npm", "--prefix", "..", "run", "ui:audit"}, []string{makefile, packageManifest}, []string{"npm.audit.frontend"})
	addCommand("npm.audit.frontend", []string{"npm", "audit", "--audit-level=high"}, []string{packageManifest, "package-lock.json"}, nil)

	return purposeEvidenceGraph{
		Leaves: leaves,
		Aggregates: map[string][]string{
			"test":                      {"test-unit", "test-integration"},
			"test-browser":              {"test-browser-workflows", "test-browser-privacy", "test-browser-visual", "test-browser-accessibility", "test-browser-cross"},
			"test-frontend-development": {"test-frontend-development-node", "test-frontend-development-browser"},
		},
		Commands: commands,
		FinalLeaves: []string{
			"test-unit", "test-integration", "test-e2e", "test-security", "test-stress", "test-keyring-native",
			"test-browser-workflows", "test-browser-privacy", "test-browser-visual", "test-browser-accessibility", "test-browser-cross",
			"test-frontend-development-node", "test-frontend-development-browser", "frontend-typecheck", "frontend-verify-supply-chain", "frontend-audit",
		},
	}
}

func validatePurposeEvidenceDAG(dag purposeEvidenceGraph) error {
	for id, leaf := range dag.Leaves {
		if leaf.ID != id || len(leaf.BehaviorIDs) == 0 || leaf.Command == "" || len(leaf.DefinitionFiles) == 0 || leaf.Timeout <= 0 || leaf.Budget < leaf.Timeout || leaf.Repeats <= 0 || leaf.GatewayStarts < 0 || leaf.BrowserStarts < 0 || len(leaf.Artifacts) == 0 || len(leaf.Cleanup) == 0 {
			return fmt.Errorf("purpose leaf %s has incomplete metadata", id)
		}
		if _, ok := dag.Commands[leaf.Command]; !ok {
			return fmt.Errorf("purpose leaf %s references unknown command %s", id, leaf.Command)
		}
	}
	for id, command := range dag.Commands {
		if command.ID != id || len(command.Argv) == 0 || len(command.DefinitionFiles) == 0 {
			return fmt.Errorf("purpose command %s has incomplete metadata", id)
		}
		for _, dependency := range command.Dependencies {
			if _, ok := dag.Commands[dependency]; !ok {
				return fmt.Errorf("purpose command %s references unknown dependency %s", id, dependency)
			}
		}
	}
	for aggregate := range dag.Aggregates {
		if _, exists := dag.Leaves[aggregate]; exists {
			return fmt.Errorf("purpose node %s is both a leaf and aggregate", aggregate)
		}
		if _, err := expandPurposeLeaves(dag, []string{aggregate}); err != nil {
			return err
		}
	}
	for _, id := range dag.FinalLeaves {
		if _, ok := dag.Leaves[id]; !ok {
			return fmt.Errorf("final purpose owner %s is not a direct leaf", id)
		}
	}
	leaves, err := expandPurposeLeaves(dag, dag.FinalLeaves)
	if err != nil {
		return err
	}
	if _, err := expandPurposeCommands(dag, leaves); err != nil {
		return err
	}
	return nil
}

func expandPurposeLeaves(dag purposeEvidenceGraph, ids []string) ([]purposeEvidenceLeaf, error) {
	var leaves []purposeEvidenceLeaf
	seenLeaves := make(map[string]struct{})
	visitingAggregates := make(map[string]struct{})
	var visit func(string) error
	visit = func(id string) error {
		if leaf, ok := dag.Leaves[id]; ok {
			if _, duplicate := seenLeaves[id]; duplicate {
				return fmt.Errorf("duplicate leaf %s", id)
			}
			seenLeaves[id] = struct{}{}
			leaves = append(leaves, leaf)
			return nil
		}
		members, ok := dag.Aggregates[id]
		if !ok {
			return fmt.Errorf("unknown purpose node %s", id)
		}
		if _, cycle := visitingAggregates[id]; cycle {
			return fmt.Errorf("purpose aggregate cycle at %s", id)
		}
		visitingAggregates[id] = struct{}{}
		for _, member := range members {
			if err := visit(member); err != nil {
				return err
			}
		}
		delete(visitingAggregates, id)
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return leaves, nil
}

func expandPurposeCommands(dag purposeEvidenceGraph, leaves []purposeEvidenceLeaf) ([]purposeCommandNode, error) {
	var commands []purposeCommandNode
	seen := make(map[string]struct{})
	visiting := make(map[string]struct{})
	var visit func(string) error
	visit = func(id string) error {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate transitive command %s", id)
		}
		if _, cycle := visiting[id]; cycle {
			return fmt.Errorf("purpose command cycle at %s", id)
		}
		command, ok := dag.Commands[id]
		if !ok {
			return fmt.Errorf("unknown purpose command %s", id)
		}
		visiting[id] = struct{}{}
		seen[id] = struct{}{}
		commands = append(commands, command)
		for _, dependency := range command.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, id)
		return nil
	}
	for _, leaf := range leaves {
		if err := visit(leaf.Command); err != nil {
			return nil, err
		}
	}
	return commands, nil
}
