package acceptance

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseProfileDefinitions(t *testing.T) {
	profile, err := finalReleaseProfile(repositoryRoot(t))
	require.NoError(t, err)
	require.NoError(t, validateFinalReleaseProfile(profile))
	assert.Equal(t, canonicalReleaseProductBehaviors(), profile.Coverage.ProductBehaviors)
	assert.Equal(t, canonicalReleaseCleanupCriteria(), profile.Coverage.CleanupCriteria)
	assert.Len(t, profile.Coverage.ProductBehaviors, 232)
	assert.Len(t, profile.Checks, 21)
}

func TestFinalReleaseProfileCoversEveryBehaviorExactlyOnce(t *testing.T) {
	profile, err := finalReleaseProfile(repositoryRoot(t))
	require.NoError(t, err)
	require.NoError(t, validateFinalReleaseProfile(profile))

	expectedChecks := []string{
		"repository-format", "repository-verify", "test-unit", "test-integration", "test-e2e", "test-security", "test-stress", "test-keyring-native",
		"test-browser-workflows", "test-browser-privacy", "test-browser-visual", "test-browser-accessibility", "test-browser-cross",
		"test-frontend-development-node", "test-frontend-development-browser", "frontend-typecheck", "frontend-verify-supply-chain", "go-vulnerability", "frontend-audit", "repository-other-tools", "repository-diff",
	}
	actualChecks := make([]string, len(profile.Checks))
	productOwners := make(map[string]int)
	cleanupOwners := make(map[string]int)
	checksByID := make(map[string]releaseCheckDefinition, len(profile.Checks))
	for index, check := range profile.Checks {
		actualChecks[index] = check.ID
		checksByID[check.ID] = check
		assert.NotEmpty(t, check.Coverage.ProductBehaviors, check.ID)
		for _, id := range check.Coverage.ProductBehaviors {
			productOwners[id]++
		}
		for _, id := range check.Coverage.CleanupCriteria {
			cleanupOwners[id]++
		}
	}
	assert.Equal(t, expectedChecks, actualChecks)
	assert.Equal(t, canonicalReleaseProductBehaviors(), profile.Coverage.ProductBehaviors)
	assert.Equal(t, canonicalReleaseCleanupCriteria(), profile.Coverage.CleanupCriteria)
	assert.Len(t, profile.Coverage.ProductBehaviors, 232)
	assert.Len(t, profile.Coverage.CleanupCriteria, 10)
	for _, id := range profile.Coverage.ProductBehaviors {
		assert.Equal(t, 1, productOwners[id], id)
	}
	for _, id := range profile.Coverage.CleanupCriteria {
		assert.Equal(t, 1, cleanupOwners[id], id)
	}
	assert.Equal(t, releaseExternalTestDefinitions(), profile.ExternalEvidence)
	assert.ElementsMatch(t, []string{
		"product.grant_request.conflict_and_uncertainty", "product.grant_request.approval_narrowing", "product.invocation.page_coherence",
		"product.client_refresh.no_unsafe_replay", "product.invocation.missing_terminal_unknown",
	}, checksByID["test-stress"].Coverage.ProductBehaviors)
	assert.Subset(t, checksByID["test-frontend-development-browser"].Coverage.ProductBehaviors, []string{"frontend.style_live_reload", "frontend.module_live_reload", "frontend.control_plane", "frontend.streaming_uncertainty"})
	assert.Contains(t, checksByID["test-e2e"].Coverage.ProductBehaviors, "tier.e2e.complete")
	assert.Contains(t, checksByID["test-unit"].Coverage.ProductBehaviors, "docs.topic.frontend.development")
	assert.Contains(t, checksByID["test-unit"].Coverage.ProductBehaviors, "product.compatibility.release_evidence")
	assert.Contains(t, checksByID["test-unit"].Coverage.ProductBehaviors, "cli.documentation")
	assert.Contains(t, checksByID["test-unit"].Coverage.CleanupCriteria, "cleanup.AC-1")
	assert.Contains(t, checksByID["test-unit"].Coverage.CleanupCriteria, "cleanup.AC-10")
	assert.NotContains(t, checksByID["repository-format"].Coverage.ProductBehaviors, "docs.topic.frontend.development")
	assert.Contains(t, checksByID["repository-verify"].Coverage.ProductBehaviors, "tier.repository.verify")
	assert.Contains(t, checksByID["go-vulnerability"].Coverage.ProductBehaviors, "tier.supply_chain.go")
	assert.Contains(t, checksByID["repository-other-tools"].Coverage.ProductBehaviors, "tier.repository.other_tools")
	assert.Contains(t, checksByID["repository-diff"].Coverage.ProductBehaviors, "tier.repository.diff")
}

func TestFinalReleaseProfileBindsMultiplicityBudgetsAndCleanup(t *testing.T) {
	profile, err := finalReleaseProfile(repositoryRoot(t))
	require.NoError(t, err)
	assert.Equal(t, int64((15 * time.Minute).Milliseconds()), profile.BudgetMillis)

	var gatewayStarts, browserStarts int
	for _, check := range profile.Checks {
		assert.Positive(t, check.TimeoutMillis, check.ID)
		assert.GreaterOrEqual(t, check.BudgetMillis, check.TimeoutMillis, check.ID)
		assert.Equal(t, []string{"processes", "listeners", "temporary roots"}, check.CleanupRequirements, check.ID)
		gatewayStarts += check.ExpectedGatewayStarts
		browserStarts += check.ExpectedBrowserStarts
		if check.ID == "test-stress" {
			assert.Equal(t, 20, check.Repeats)
			assert.Equal(t, []string{"make", "-C", "mcp-gateway", "STRESS_COUNT=20", "test-stress"}, check.Argv)
			continue
		}
		if check.ID == "test-keyring-native" {
			assert.Equal(t, []string{"mcp-gateway/test/keyring-native.sh"}, check.Argv)
		}
		assert.Equal(t, 1, check.Repeats, check.ID)
	}
	assert.Equal(t, 105, gatewayStarts)
	assert.Equal(t, 40, browserStarts)
	assert.Equal(t, 1, countReleaseChecksContaining(profile.Checks, "test-e2e"))
	assert.Equal(t, 1, countReleaseChecksContaining(profile.Checks, "verify-supply-chain"))
	for _, aggregate := range []string{"test", "test-browser", "test-frontend-development", "frontend-build", "frontend-verify-generated"} {
		assert.NotContains(t, releaseCheckIDs(profile.Checks), aggregate)
	}
}

func TestFinalReleaseProfileHashesAreStableAndOrderSensitive(t *testing.T) {
	first, err := finalReleaseProfile(repositoryRoot(t))
	require.NoError(t, err)
	second, err := finalReleaseProfile(repositoryRoot(t))
	require.NoError(t, err)
	firstProfile, firstDefinitions, firstManifest, err := releaseDefinitionHashes(repositoryRoot(t), first)
	require.NoError(t, err)
	secondProfile, secondDefinitions, secondManifest, err := releaseDefinitionHashes(repositoryRoot(t), second)
	require.NoError(t, err)
	assert.Equal(t, firstProfile, secondProfile)
	assert.Equal(t, firstDefinitions, secondDefinitions)
	assert.Equal(t, firstManifest, secondManifest)

	slices.Reverse(second.Checks)
	reorderedProfile, reorderedDefinitions, unchangedManifest, err := releaseDefinitionHashes(repositoryRoot(t), second)
	require.NoError(t, err)
	assert.NotEqual(t, firstProfile, reorderedProfile)
	assert.NotEqual(t, firstDefinitions, reorderedDefinitions)
	assert.Equal(t, firstManifest, unchangedManifest)

	for _, required := range []string{"mcp-gateway/test/acceptance/release_profile.go", "mcp-gateway/test/acceptance/release_report.schema.json", "mcp-gateway/test/acceptance/release_external_evidence.schema.json", "mcp-gateway/web/environments.json", "mcp-gateway/Makefile", "Makefile", "package.json", "package-lock.json"} {
		assert.Contains(t, first.DefinitionFiles, required)
	}
}

func TestFinalReleaseProfileRejectsCoverageMultiplicityAndCleanupDrift(t *testing.T) {
	profile, err := finalReleaseProfile(repositoryRoot(t))
	require.NoError(t, err)

	missing := cloneReleaseProfile(profile)
	missing.Checks[0].Coverage.ProductBehaviors = missing.Checks[0].Coverage.ProductBehaviors[1:]
	assert.ErrorContains(t, validateFinalReleaseProfile(missing), "exactly one")

	duplicate := cloneReleaseProfile(profile)
	duplicate.Checks[1].Coverage.ProductBehaviors = append(duplicate.Checks[1].Coverage.ProductBehaviors, duplicate.Checks[0].Coverage.ProductBehaviors[0])
	assert.ErrorContains(t, validateFinalReleaseProfile(duplicate), "exactly one")

	stress := cloneReleaseProfile(profile)
	index := slices.IndexFunc(stress.Checks, func(check releaseCheckDefinition) bool { return check.ID == "test-stress" })
	stress.Checks[index].Repeats = 1
	assert.ErrorContains(t, validateFinalReleaseProfile(stress), "stress")

	aggregate := cloneReleaseProfile(profile)
	aggregate.Checks[0].Argv = []string{"make", "-C", "mcp-gateway", "test-browser"}
	assert.ErrorContains(t, validateFinalReleaseProfile(aggregate), "closed direct command")

	cleanup := cloneReleaseProfile(profile)
	cleanup.Checks[0].CleanupRequirements = []string{"processes"}
	assert.ErrorContains(t, validateFinalReleaseProfile(cleanup), "cleanup")
}

func countReleaseChecksContaining(checks []releaseCheckDefinition, fragment string) int {
	count := 0
	for _, check := range checks {
		if strings.Contains(strings.Join(check.Argv, " "), fragment) {
			count++
		}
	}
	return count
}

func releaseCheckIDs(checks []releaseCheckDefinition) []string {
	ids := make([]string, len(checks))
	for index, check := range checks {
		ids[index] = check.ID
	}
	return ids
}
