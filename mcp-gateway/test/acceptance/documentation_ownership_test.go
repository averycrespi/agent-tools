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
