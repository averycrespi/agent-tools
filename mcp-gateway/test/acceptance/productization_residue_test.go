package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishedMaintainerSurfacesContainNoImplementationProgramResidue(t *testing.T) {
	root := repositoryRoot(t)
	var paths []string
	for _, relative := range []string{"README.md", "CLAUDE.md", "Makefile", "mcp-gateway/README.md", "mcp-gateway/DESIGN.md", "mcp-gateway/CLAUDE.md", "mcp-gateway/Makefile"} {
		paths = append(paths, filepath.Join(root, relative))
	}
	docs, err := filepath.Glob(filepath.Join(root, "mcp-gateway", "docs", "*.md"))
	require.NoError(t, err)
	paths = append(paths, docs...)
	for _, relative := range []string{
		"mcp-gateway/internal/contract/control_plane_capabilities.go", "mcp-gateway/internal/contract/documentation_ownership.go", "mcp-gateway/internal/contract/product_behavior_manifest.go",
		"mcp-gateway/test/acceptance/acceptance.go", "mcp-gateway/test/acceptance/purpose_dag.go", "mcp-gateway/test/acceptance/release_external_evidence.go",
		"mcp-gateway/test/acceptance/release_profile.go", "mcp-gateway/test/acceptance/release_report.go", "mcp-gateway/test/acceptance/release_runner.go", "mcp-gateway/test/acceptance/cmd/main.go",
	} {
		paths = append(paths, filepath.Join(root, relative))
	}
	residue := regexp.MustCompile(`(?i)(?:\bS[1-6]\b|accept-s[0-9]|test-(?:task|milestone)|task owner|milestone owner|planned.{0,20}executable|--(?:profile|task|milestone)\b)`)
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr, path)
		text := strings.ReplaceAll(string(contents), "mcp-gateway/s2", "mcp-gateway/client")
		text = strings.ReplaceAll(text, ".design/acceptance/s6/", ".design/acceptance/retired/")
		if strings.HasSuffix(path, filepath.Join("cmd", "main.go")) {
			for _, fixture := range []string{`"--profile"`, `"--task"`, `"--milestone"`} {
				text = strings.ReplaceAll(text, fixture, `"removed-mode"`)
			}
		}
		assert.NotRegexp(t, residue, text, path)
	}
}

func TestTrackedGoTestNamesContainNoImplementationProgramOwners(t *testing.T) {
	root := filepath.Join(repositoryRoot(t), "mcp-gateway")
	nameResidue := regexp.MustCompile(`(?i)(?:S[1-6]|Task|Milestone|Phase)`)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == "node_modules" || entry.Name() == "dist") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && strings.HasPrefix(function.Name.Name, "Test") {
				assert.NotRegexp(t, nameResidue, function.Name.Name, path)
			}
		}
		return nil
	}))
}
