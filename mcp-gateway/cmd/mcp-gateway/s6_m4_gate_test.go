package main

import (
	"bytes"
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6M4Gate(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	require.True(t, ok)
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))

	allowedInternal := map[string]map[string]bool{
		"internal/controlclient": {
			"github.com/averycrespi/agent-tools/mcp-gateway/internal/paths":      true,
			"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson": true,
		},
		"cmd/mcp-gateway": {
			"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract":      true,
			"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient": true,
		},
	}
	for _, relative := range []string{"internal/controlclient", "cmd/mcp-gateway"} {
		entries, err := os.ReadDir(filepath.Join(moduleRoot, relative))
		require.NoError(t, err)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			if relative == "cmd/mcp-gateway" && !strings.HasPrefix(entry.Name(), "online") {
				continue
			}
			path := filepath.Join(moduleRoot, relative, entry.Name())
			source, err := os.ReadFile(path)
			require.NoError(t, err)
			parsed, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
			require.NoError(t, err)
			for _, imported := range parsed.Imports {
				name, err := strconv.Unquote(imported.Path.Value)
				require.NoError(t, err)
				if strings.Contains(name, "/internal/") {
					assert.True(t, allowedInternal[relative][name], "%s imports private authority %s", entry.Name(), name)
				}
			}
			if relative == "cmd/mcp-gateway" {
				assert.NotContains(t, string(source), "http.Client{")
				assert.NotContains(t, string(source), "http.Transport{")
			}
		}
	}

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := newRootCmd()
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"restore"})
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Empty(t, stdout.String())
	assert.Equal(t, "Use --verify-current with no backup ID, or provide exactly one valid backup ID.\n", stderr.String())
}
