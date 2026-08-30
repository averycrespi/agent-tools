package acceptance

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentationGuideOwnership(t *testing.T) {
	root := frontendDevelopmentModuleRoot(t)
	guides := contract.DocumentationGuideManifest()
	expectedPaths := make([]string, 0, len(guides))
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	require.NoError(t, err)

	for _, guide := range guides {
		expectedPaths = append(expectedPaths, guide.Path)
		contents, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(guide.Path)))
		require.NoError(t, readErr, guide.Path)
		text := string(contents)
		assert.Contains(t, text, "Audience: "+guide.Audience, guide.Path)
		assert.Contains(t, text, "Purpose: "+guide.Purpose, guide.Path)
		assert.Contains(t, string(readme), "("+guide.Path+")", guide.Path)
		assertMarkdownLinksResolve(t, filepath.Join(root, filepath.FromSlash(guide.Path)), text)
	}

	matches, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	require.NoError(t, err)
	actualPaths := make([]string, 0, len(matches))
	for _, match := range matches {
		relative, relErr := filepath.Rel(root, match)
		require.NoError(t, relErr)
		actualPaths = append(actualPaths, filepath.ToSlash(relative))
	}
	sort.Strings(actualPaths)
	sort.Strings(expectedPaths)
	assert.Equal(t, expectedPaths, actualPaths, "focused guides must have exactly one manifest owner")
}

func TestDocumentationOwnersAreClosed(t *testing.T) {
	owners := make(map[string]int)
	for _, command := range contract.DocumentationCommandManifest() {
		owners[command.ID]++
	}
	for _, sensitive := range contract.DocumentationSecurityManifest() {
		owners[sensitive.ID]++
		commandFamilies := make(map[string]struct{}, len(sensitive.HelpFamilies))
		for _, family := range sensitive.HelpFamilies {
			_, duplicate := commandFamilies[family]
			assert.False(t, duplicate, sensitive.ID+":"+family)
			commandFamilies[family] = struct{}{}
		}
	}
	for id, count := range owners {
		assert.Equal(t, 1, count, id)
	}
}

func TestFreshUserDocumentationGraph(t *testing.T) {
	root := frontendDevelopmentModuleRoot(t)
	readmeBytes, err := os.ReadFile(filepath.Join(root, "README.md"))
	require.NoError(t, err)
	readme := string(readmeBytes)
	assert.Less(t, len(readmeBytes), 12000, "README must remain a concise entry point")
	for _, heading := range []string{"## Installation", "## Quick start", "## Common workflows", "## Security", "## Guides", "## Development"} {
		assert.Contains(t, readme, heading)
	}
	for _, command := range []string{"make install", "mcp-gateway initialize", "mcp-gateway serve", "mcp-gateway status"} {
		assert.Contains(t, readme, command)
	}
	for _, guide := range contract.DocumentationGuideManifest() {
		assert.Contains(t, readme, "("+guide.Path+")", guide.ID)
	}
	for _, detailed := range []string{"XDG_DATA_HOME", "--admin-bearer-stdin", "--verify-current", "schema 10"} {
		assert.NotContains(t, readme, detailed, "detailed contracts belong in focused guides")
	}
	assert.NotRegexp(t, regexp.MustCompile(`(?i)(?:\bS[1-6]\b|\bT[0-9]+\b|\bM[0-9]+\b|planned|executable|milestone|implementation phase)`), readme)

	rootReadmeBytes, err := os.ReadFile(filepath.Join(filepath.Dir(root), "README.md"))
	require.NoError(t, err)
	rootReadme := string(rootReadmeBytes)
	start := strings.Index(rootReadme, "### MCP Gateway")
	require.NotEqual(t, -1, start)
	end := strings.Index(rootReadme[start+len("### MCP Gateway"):], "\n### ")
	require.NotEqual(t, -1, end)
	gatewayOverview := rootReadme[start : start+len("### MCP Gateway")+end]
	assert.Less(t, len(gatewayOverview), 1800)
	assert.NotContains(t, gatewayOverview, "being built")
	assert.NotRegexp(t, regexp.MustCompile(`\bS[1-6]\b`), gatewayOverview)
	assert.Contains(t, gatewayOverview, "mcp-gateway/README.md")
	assert.Contains(t, gatewayOverview, "mcp-gateway/docs/cli-local-administration.md")
}

func TestCLIAndRecoveryGuidesOwnDetailedContracts(t *testing.T) {
	root := frontendDevelopmentModuleRoot(t)
	read := func(path string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(root, path))
		require.NoError(t, err)
		return string(contents)
	}
	cli := read("docs/cli-local-administration.md")
	for _, phrase := range []string{
		"$XDG_DATA_HOME/mcp-gateway", "~/.local/share/mcp-gateway", "`--data-dir` has highest precedence",
		"Online administrator authentication never prompts", "--admin-bearer-file", "--admin-bearer-stdin",
		"Human output is the default", "`--output json`", "stdout", "stderr", "typed exit",
		"http://127.0.0.1:8210", "never accepted in argv or environment", "never retries automatically",
	} {
		assert.Contains(t, cli, phrase)
	}
	recovery := read("docs/recovery.md")
	for _, phrase := range []string{
		"mcp-gateway backup create", "mcp-gateway restore --verify-current", "mcp-gateway restore BACKUP_ID",
		"mcp-gateway admin-reset", "Gateway must be stopped", "--secret-output", "--admin-bearer-file",
		"invalidates every restored agent credential", "does not rewrite the default `admin-bearer`", "Failed commands leave stdout empty",
	} {
		assert.Contains(t, recovery, phrase)
	}
}

func TestOperationalGuidesCoverBehaviorManifest(t *testing.T) {
	root := frontendDevelopmentModuleRoot(t)
	read := func(path string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(root, path))
		require.NoError(t, err)
		return string(contents)
	}
	guides := map[string]string{
		"product.server_catalog.": read("docs/server-configuration.md"),
		"product.access_policy.":  read("docs/access-policy.md"),
		"product.grant_request.":  read("docs/access-policy.md"),
		"product.invocation.":     read("docs/invocation-evidence.md"),
	}
	markers := map[string]string{
		"product.server_catalog.state_separation":           "desired configuration, durable catalog evidence, and active runtime state",
		"product.server_catalog.safe_server_registration":   "mcp-gateway server create --file PATH",
		"product.server_catalog.etag_updates":               "--etag ETAG",
		"product.server_catalog.eligible_operations":        "reload`, `retry`, `refresh_catalog`, or `disconnect_credentials",
		"product.server_catalog.operation_polling":          "does not poll automatically",
		"product.server_catalog.write_only_credentials":     "write-only",
		"product.server_catalog.one_time_oauth_url":         "one-time authorization URL",
		"product.server_catalog.active_vs_durable_catalog":  "evidence, not a callability claim",
		"product.server_catalog.deletion_and_disconnect":    "Deletion is permanent",
		"product.access_policy.principal_inventory":         "mcp-gateway principal list",
		"product.access_policy.principal_creation_defaults": "synthetic default grant",
		"product.access_policy.agent_credential_rotation":   "old bearer never overlaps",
		"product.access_policy.immutable_grants":            "Grants are immutable",
		"product.access_policy.closed_constraints":          "object-only RFC 6901",
		"product.access_policy.grant_correction_order":      "Create before delete produces temporary overlap; delete before create produces temporary loss",
		"product.access_policy.expired_and_default_grants":  "Expired grants remain readable",
		"product.grant_request.pending_collection":          "state pending",
		"product.grant_request.evidence_item":               "item-only evidence",
		"product.grant_request.approval_narrowing":          "Approval may only narrow",
		"product.grant_request.rejection_contract":          "scope_too_broad",
		"product.grant_request.conflict_and_uncertainty":    "never executes, resumes, or replays",
		"product.grant_request.historical_approval":         "historical evidence only",
		"product.invocation.read_only_routes":               "read-only",
		"product.invocation.closed_filters":                 "principal, server, requested-name, admission, decision, and outcome filters",
		"product.invocation.newest_first_cursor":            "newest-first",
		"product.invocation.page_coherence":                 "stale_cursor",
		"product.invocation.summary_projection":             "Collections omit argument captures",
		"product.invocation.item_redacted_arguments":        "fixed-redacted",
		"product.invocation.synthetic_target_derivation":    "Gateway-local targets",
		"product.invocation.outcome_derivation":             "invalid_params",
		"product.invocation.missing_terminal_unknown":       "Missing terminal evidence",
		"product.invocation.local_target_unknown":           "local storage uncertainty",
		"product.invocation.bounded_retention":              "4,096",
		"product.invocation.polling_without_authority":      "Polling never submits",
	}
	seen := 0
	for _, behavior := range contract.ProductBehaviorManifest() {
		if behavior.Kind != "clause" {
			continue
		}
		for prefix, guide := range guides {
			if !strings.HasPrefix(behavior.ID, prefix) {
				continue
			}
			marker, ok := markers[behavior.ID]
			require.True(t, ok, behavior.ID)
			assert.Contains(t, guide, marker, behavior.ID)
			seen++
		}
	}
	assert.Equal(t, len(markers), seen)
	accessGuide := guides["product.access_policy."]
	assert.Contains(t, accessGuide, "`all` discovers every current tool")
	assert.Contains(t, accessGuide, "canonical decimal from 60 through 2,592,000 seconds")
	for _, tool := range []string{"get_identity", "list_grants", "create_grant_request", "get_grant_request", "list_grant_requests", "cancel_grant_request"} {
		assert.Contains(t, accessGuide, "mcp_gateway."+tool)
	}

	for path, guide := range map[string]string{
		"server":     guides["product.server_catalog."],
		"access":     guides["product.access_policy."],
		"invocation": guides["product.invocation."],
	} {
		assert.Contains(t, guide, "[DESIGN](../DESIGN.md)", path)
		assert.NotContains(t, guide, "/api/v1/", path+" should link to normative routes rather than copy them")
		assert.NotRegexp(t, regexp.MustCompile(`(?i)(?:\bS[1-6]\b|\bT[0-9]+\b|\bM[0-9]+\b|planned|executable|milestone|implementation phase)`), guide, path)
	}
}

func TestMaintainerGuidanceAndReleaseDocumentation(t *testing.T) {
	root := frontendDevelopmentModuleRoot(t)
	read := func(path string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		require.NoError(t, err)
		return string(contents)
	}
	gatewayGuidance := read("CLAUDE.md")
	rootGuidance := read("../CLAUDE.md")
	release := read("docs/release-verification.md")
	frontend := read("docs/frontend-development.md")

	assert.Less(t, len(gatewayGuidance), 20000, "maintainer guidance must link to product contracts rather than copy them")
	for _, heading := range []string{"## Development", "## Package layout", "## Editing invariants", "## Dependency flow"} {
		assert.Contains(t, gatewayGuidance, heading)
	}
	for _, link := range []string{"docs/frontend-development.md", "docs/release-verification.md"} {
		assert.Contains(t, gatewayGuidance, link)
		assert.Contains(t, rootGuidance, "mcp-gateway/"+link)
	}
	assert.NotContains(t, gatewayGuidance, "## Quick start")

	for _, phrase := range []string{
		"## Purpose-based verification DAG", "`test` aggregates `test-unit` and `test-integration` only",
		"`accept` invokes disjoint leaves directly", "complete nonbrowser E2E", "five named stress scenarios",
		"## CI mapping", "superseded report definitions are incompatible", "candidate revision",
		"typed `passed`, `skipped`, or `failed`", "blocking", "additive", "qualify-external-evidence",
		"## Failure discipline", "Do not rerun `accept` unchanged", "## Report adoption", "no-check adoption",
		"does not rerun product checks",
	} {
		assert.Contains(t, release, phrase)
	}
	for _, phrase := range []string{
		"two independently owned loopback processes", "trusted local process", "OAuth callback remains on the Gateway origin",
		"Production is separate and deterministic", "npm run ui:dev", "npm run ui:build", "internal/api/static",
	} {
		assert.Contains(t, frontend, phrase)
	}

	residue := regexp.MustCompile(`(?i)(?:\bS[1-6]\b|\bT[0-9]+\b|\bM[0-9]+\b|accept-s[0-9]|test-task|test-milestone|task owner|milestone owner|planned.{0,20}executable)`)
	for path, document := range map[string]string{"CLAUDE.md": gatewayGuidance, "../CLAUDE.md": rootGuidance, "release": release, "frontend": frontend} {
		assert.NotRegexp(t, residue, document, path)
	}
}

func assertMarkdownLinksResolve(t *testing.T, path, contents string) {
	t.Helper()
	links := regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`).FindAllStringSubmatch(contents, -1)
	for _, match := range links {
		target := strings.Split(match[1], "#")[0]
		if target == "" || strings.Contains(target, "://") {
			continue
		}
		_, err := os.Stat(filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target))))
		assert.NoError(t, err, "%s -> %s", path, match[1])
	}
}
