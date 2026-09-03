package contract

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentationOwnership(t *testing.T) {
	t.Run("schema", testDocumentationOwnershipManifestSchema)
	t.Run("independent values", testDocumentationOwnershipManifestReturnsIndependentValues)
}

func testDocumentationOwnershipManifestSchema(t *testing.T) {
	assert.Equal(t, 1, DocumentationOwnershipManifestVersion)
	guides := DocumentationGuideManifest()
	commands := DocumentationCommandManifest()
	security := DocumentationSecurityManifest()
	require.Len(t, guides, 7)
	require.Len(t, commands, 13)
	require.Len(t, security, 8)

	guidePaths := make(map[string]struct{}, len(guides))
	ids := make(map[string]struct{}, len(guides)+len(commands)+len(security))
	validID := regexp.MustCompile(`^docs\.[a-z0-9]+(?:\.[a-z0-9]+)*$`)
	addID := func(id string) {
		t.Helper()
		assert.Regexp(t, validID, id)
		_, duplicate := ids[id]
		assert.False(t, duplicate, id)
		ids[id] = struct{}{}
	}
	for _, guide := range guides {
		addID(guide.ID)
		assert.Regexp(t, `^docs/(?:operators|maintainers)/[a-z0-9-]+\.md$`, guide.Path)
		assert.NotEmpty(t, guide.Audience, guide.ID)
		assert.NotEmpty(t, guide.Purpose, guide.ID)
		_, duplicate := guidePaths[guide.Path]
		assert.False(t, duplicate, guide.Path)
		guidePaths[guide.Path] = struct{}{}
	}
	commandPaths := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		addID(command.ID)
		_, knownOwner := guidePaths[command.CanonicalOwner]
		assert.True(t, knownOwner, command.ID)
		assert.Equal(t, "mcp-gateway "+command.CommandPath+" --help", command.HelpInvocation, command.ID)
		_, duplicate := commandPaths[command.CommandPath]
		assert.False(t, duplicate, command.CommandPath)
		commandPaths[command.CommandPath] = struct{}{}
	}
	for _, contract := range security {
		addID(contract.ID)
		_, knownOwner := guidePaths[contract.CanonicalOwner]
		assert.True(t, knownOwner, contract.ID)
		require.NotEmpty(t, contract.HelpFamilies, contract.ID)
		for _, family := range contract.HelpFamilies {
			_, knownFamily := commandPaths[family]
			assert.True(t, knownFamily, contract.ID+":"+family)
		}
	}
}

func testDocumentationOwnershipManifestReturnsIndependentValues(t *testing.T) {
	guides := DocumentationGuideManifest()
	guides[0].Path = "changed"
	assert.NotEqual(t, "changed", DocumentationGuideManifest()[0].Path)

	commands := DocumentationCommandManifest()
	commands[0].CanonicalOwner = "changed"
	assert.NotEqual(t, "changed", DocumentationCommandManifest()[0].CanonicalOwner)

	security := DocumentationSecurityManifest()
	security[0].HelpFamilies[0] = "changed"
	assert.NotEqual(t, "changed", DocumentationSecurityManifest()[0].HelpFamilies[0])
}
