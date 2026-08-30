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
