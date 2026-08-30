package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIControlBoundary(t *testing.T) {
	commandRoot := filepath.Join(repositoryRoot(t), "mcp-gateway", "cmd", "mcp-gateway")
	onlineSource, err := os.ReadFile(filepath.Join(commandRoot, "online.go"))
	require.NoError(t, err)
	source := string(onlineSource)
	for _, owner := range []string{
		`onlineSpec([]string{"server", "create"}`,
		`onlineSpec([]string{"server", "update"}`,
		`onlineSpec([]string{"server", "credential", "replace"}`,
		`onlineSpec([]string{"grant", "create"}`,
		`onlineSpec([]string{"grant-request", "approve"}`,
	} {
		line := sourceLineContaining(source, owner)
		assert.Contains(t, line, `"file"`, owner)
	}
	for _, owner := range []string{
		`onlineSpec([]string{"admin", "credential", "create"}`,
		`onlineSpec([]string{"principal", "create"}`,
		`onlineSpec([]string{"principal", "update"}`,
		`onlineSpec([]string{"server", "operation", "start"}`,
		`onlineSpec([]string{"grant-request", "reject"}`,
	} {
		line := sourceLineContaining(source, owner)
		require.NotEmpty(t, line, owner)
		assert.NotContains(t, line, `"file"`, owner)
	}

	rootSource, err := os.ReadFile(filepath.Join(commandRoot, "root.go"))
	require.NoError(t, err)
	assert.Contains(t, string(rootSource), `newAdminAuthorityCmd("reset", dependencies)`)
	assert.NotContains(t, source, `onlineSpec([]string{"admin", "reset"}`)
}

func sourceLineContaining(source, needle string) string {
	for _, line := range strings.Split(source, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
