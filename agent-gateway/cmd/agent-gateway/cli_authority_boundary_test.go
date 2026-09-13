package main

import (
	"bytes"
	"context"
	"go/ast"
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

func TestCLIPrivateAuthorityBoundary(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	require.True(t, ok)
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))

	allowedInternal := map[string]map[string]bool{
		"internal/controlclient": {
			"github.com/averycrespi/agent-tools/agent-gateway/internal/contract":   true,
			"github.com/averycrespi/agent-tools/agent-gateway/internal/paths":      true,
			"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson": true,
		},
		"cmd/agent-gateway": {
			"github.com/averycrespi/agent-tools/agent-gateway/internal/contract":      true,
			"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient": true,
			"github.com/averycrespi/agent-tools/agent-gateway/internal/paths":         true,
		},
	}
	for _, relative := range []string{"internal/controlclient", "cmd/agent-gateway"} {
		entries, err := os.ReadDir(filepath.Join(moduleRoot, relative))
		require.NoError(t, err)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			if relative == "cmd/agent-gateway" && !strings.HasPrefix(entry.Name(), "online") {
				continue
			}
			path := filepath.Join(moduleRoot, relative, entry.Name())
			source, err := os.ReadFile(path)
			require.NoError(t, err)
			parsed, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
			require.NoError(t, err)
			for _, imported := range parsed.Imports {
				name, err := strconv.Unquote(imported.Path.Value)
				require.NoError(t, err)
				if strings.Contains(name, "/internal/") {
					assert.True(t, allowedInternal[relative][name], "%s imports private authority %s", entry.Name(), name)
				}
			}
			if relative == "internal/controlclient" {
				for _, imported := range parsed.Imports {
					if strings.Trim(imported.Path.Value, "\"") == "github.com/averycrespi/agent-tools/agent-gateway/internal/contract" {
						assert.Nil(t, imported.Name, "contract grammar import cannot be aliased")
					}
				}
				ast.Inspect(parsed, func(node ast.Node) bool {
					if selector, ok := node.(*ast.SelectorExpr); ok {
						if owner, ok := selector.X.(*ast.Ident); ok && owner.Name == "contract" {
							assert.Equal(t, "NormalizeHostname", selector.Sel.Name, "controlclient may consume only the hostname grammar")
						}
					}
					return true
				})
			}
			if relative == "cmd/agent-gateway" {
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
	command.SetArgs([]string{"backup", "restore"})
	err := command.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Empty(t, stdout.String())
	assert.Equal(t, "Provide exactly one valid backup ID. Usage: agent-gateway backup restore BACKUP_ID --secret-output NEW_PATH\n", stderr.String())
}
