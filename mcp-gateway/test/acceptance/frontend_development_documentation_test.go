package acceptance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFrontendDevelopmentDocumentation(t *testing.T) {
	root := frontendDevelopmentModuleRoot(t)
	guidePath := filepath.Join(root, "docs", "frontend-development.md")
	guideBytes, err := os.ReadFile(guidePath)
	require.NoError(t, err)
	guide := string(guideBytes)
	for _, phrase := range []string{
		"# Frontend development",
		"mcp-gateway serve",
		"npm run ui:dev",
		"http://127.0.0.1:5173",
		"MCP_GATEWAY_UI_LISTEN",
		"MCP_GATEWAY_UI_GATEWAY",
		"canonical numeric IPv4 `127/8`",
		"ports from `1` to `65535`",
		"does not accept arguments or pass-through Vite options",
		"trusted local process",
		"host-only session cookie",
		"OAuth callback remains on the Gateway origin",
		"Ctrl-C",
		"already in use",
		"502",
		"make -C mcp-gateway test-frontend-development",
		"npm run ui:build",
		"internal/api/static",
	} {
		require.Contains(t, guide, phrase)
	}

	for _, owner := range []string{"README.md", "DESIGN.md", "CLAUDE.md"} {
		contents, readErr := os.ReadFile(filepath.Join(root, owner))
		require.NoError(t, readErr)
		require.Contains(t, string(contents), "docs/frontend-development.md", owner)
	}
	rootClaude, err := os.ReadFile(filepath.Join(filepath.Dir(root), "CLAUDE.md"))
	require.NoError(t, err)
	require.Contains(t, string(rootClaude), "mcp-gateway/docs/frontend-development.md")

	developmentSource, err := os.ReadFile(filepath.Join(root, "web", "dev-server.ts"))
	require.NoError(t, err)
	for _, contract := range []string{
		`const DEFAULT_LISTEN = "127.0.0.1:5173"`,
		`const DEFAULT_GATEWAY = "http://127.0.0.1:8210"`,
		"MCP_GATEWAY_UI_LISTEN",
		"MCP_GATEWAY_UI_GATEWAY",
		"does not accept arguments or pass-through Vite options",
	} {
		require.Contains(t, string(developmentSource), contract)
	}
	packageManifest, err := os.ReadFile(filepath.Join(filepath.Dir(root), "package.json"))
	require.NoError(t, err)
	require.Contains(t, string(packageManifest), `"ui:dev": "node mcp-gateway/web/dev-server.ts"`)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	require.Contains(t, string(makefile), "test-frontend-development:")

	links := regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`).FindAllStringSubmatch(guide, -1)
	for _, match := range links {
		target := match[1]
		if strings.HasPrefix(target, "#") || strings.Contains(target, "://") {
			continue
		}
		_, statErr := os.Stat(filepath.Clean(filepath.Join(filepath.Dir(guidePath), target)))
		require.NoError(t, statErr, target)
	}
}
