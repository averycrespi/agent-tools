package composition

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type productionSource struct {
	path     string
	contents string
	file     *ast.File
	imports  map[string]string
}

func TestProductionSourceOwnershipGuards(t *testing.T) {
	root := gatewayModuleRoot(t)
	allowedExec := func(path string) bool {
		return path == "internal/keyring/probe_darwin.go" || path == "test/acceptance/acceptance.go" || strings.HasPrefix(path, "internal/runtimes/stdio")
	}
	processConstructors := map[string]string{"internal/keyring/probe_darwin.go": "CommandContext", "internal/runtimes/stdio.go": "Command", "test/acceptance/acceptance.go": "CommandContext"}
	allowedHTTP := "internal/remote/remote.go"
	allowedSDK := map[string]bool{"internal/dependencies/dependencies.go": true, "internal/mcpingress/handler.go": true}
	for _, source := range productionSources(t, root) {
		for _, imported := range source.imports {
			if strings.HasSuffix(imported, "/internal/testutil") {
				t.Errorf("%s: prohibited import %s", source.path, imported)
			}
			if imported == "os/exec" && !allowedExec(source.path) {
				t.Errorf("%s: misplaced process constructor import %s", source.path, imported)
			}
			if strings.HasPrefix(imported, "github.com/modelcontextprotocol/go-sdk/") && !allowedSDK[source.path] {
				t.Errorf("%s: prohibited SDK import %s", source.path, imported)
			}
		}
		ast.Inspect(source.file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CompositeLit:
				selector, ok := value.Type.(*ast.SelectorExpr)
				if ok && source.selectorPackage(selector) == "net/http" && (selector.Sel.Name == "Client" || selector.Sel.Name == "Transport") && source.path != allowedHTTP {
					t.Errorf("%s: misplaced HTTP constructor http.%s", source.path, selector.Sel.Name)
				}
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if ok && source.selectorPackage(selector) == "os/exec" {
					expected, owned := processConstructors[source.path]
					if !owned || selector.Sel.Name != expected {
						t.Errorf("%s: misplaced process constructor exec.%s", source.path, selector.Sel.Name)
					}
					if len(value.Args) != 0 {
						if executable, literal := value.Args[0].(*ast.BasicLit); literal && strings.Contains(strings.Trim(executable.Value, `"`), "sh") {
							t.Errorf("%s: prohibited shell constructor exec.%s", source.path, selector.Sel.Name)
						}
					}
				}
			case *ast.FuncDecl:
				if strings.EqualFold(value.Name.Name, "decodeTransport") && source.path != "internal/servers/repository.go" {
					t.Errorf("%s: duplicate transport decoder %s", source.path, value.Name.Name)
				}
			}
			return true
		})
		for _, symbol := range []string{"exec.LookPath(", "CommandTransport", "Client.Connect(", "ClientSession.ListTools", "Routes().Resolve("} {
			if strings.Contains(source.contents, symbol) {
				t.Errorf("%s: prohibited symbol %s", source.path, symbol)
			}
		}
		if source.path != "cmd/mcp-gateway/root.go" && source.path != "internal/mcpingress/handler.go" && strings.Contains(source.contents, "mcpingress.Options{") {
			t.Errorf("%s: misplaced production authenticator mcpingress.Options", source.path)
		}
	}
	probe := readProductionSource(t, root, "internal/keyring/probe_darwin.go")
	assert.Contains(t, probe, `securityTool = "/usr/bin/security"`, "internal/keyring/probe_darwin.go: keyring probe must use an absolute executable")
}

func TestProductionRootUsesConcreteNativeCompositionAndDenyAllIngress(t *testing.T) {
	root := gatewayModuleRoot(t)
	rootSource := readProductionSource(t, root, "cmd/mcp-gateway/root.go")
	compositionSource := readProductionSource(t, root, "internal/composition/composition.go")
	nativeProviderSource := readProductionSource(t, root, "internal/composition/provider_factory.go")
	e2eProviderSource := readProductionSource(t, root, "internal/composition/provider_factory_e2e.go")
	for _, required := range []string{
		"newComposition: composition.New", "newComposition(composition.Options{", "authorizationRepository := runtime.Authorization()", "Principals:  authorizationRepository", "GrantTarget: func(", "serverRepository.ValidateGrantTargetTx", "activeCatalog := runtime.ActiveCatalog()",
		"ActiveCatalog:  activeCatalog", "runtime.Start(ctx)", "runtime.Drain(shutdownCtx)", "mcpingress.DenyAllAuthenticator{}",
	} {
		assert.Contains(t, rootSource, required, "cmd/mcp-gateway/root.go: missing production symbol %s", required)
	}
	for _, prohibited := range []string{"runtimes.New(", "unavailableDriver", "absentCatalog", "newMemoryPublisher", "Routes().Resolve("} {
		assert.NotContains(t, rootSource, prohibited, "cmd/mcp-gateway/root.go: prohibited production symbol %s", prohibited)
	}
	assert.Equal(t, 1, strings.Count(rootSource, "Authenticator:"), "cmd/mcp-gateway/root.go: production authenticator must have one owner")
	assert.Contains(t, compositionSource, "providerFactory = productionProvider", "internal/composition/composition.go: ordinary build must use the build-selected provider")
	assert.Contains(t, nativeProviderSource, "//go:build !e2e", "internal/composition/provider_factory.go: native provider must exclude e2e builds")
	assert.Contains(t, nativeProviderSource, "keyring.NewProvider(installationID)", "internal/composition/provider_factory.go: ordinary build must select native provider")
	assert.Contains(t, e2eProviderSource, "//go:build e2e", "internal/composition/provider_factory_e2e.go: deterministic provider must be e2e-only")
	assert.Contains(t, e2eProviderSource, "keyring.NewProviderWithBackend", "internal/composition/provider_factory_e2e.go: e2e build must use the explicit provider boundary")
	for _, prohibited := range []string{"os.Getenv", "flag.", "cobra.", "http."} {
		assert.NotContains(t, e2eProviderSource, prohibited, "internal/composition/provider_factory_e2e.go: provider seam must have no public configuration symbol %s", prohibited)
	}
}

func TestProductionPersistenceAndCapabilitySliceGuards(t *testing.T) {
	root := gatewayModuleRoot(t)
	prohibitedColumn := regexp.MustCompile(`(?i)\b(runtime_id|process_id|pid|session_id|access_token|refresh_token|client_secret)\b`)
	migrations, err := filepath.Glob(filepath.Join(root, "internal/storage/migrations/*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, migrations)
	for _, path := range migrations {
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		if match := prohibitedColumn.Find(contents); match != nil {
			relative, relErr := filepath.Rel(root, path)
			require.NoError(t, relErr)
			t.Errorf("%s: prohibited persisted column %s", filepath.ToSlash(relative), match)
		}
	}
	for _, source := range productionSources(t, root) {
		if strings.HasPrefix(source.path, "internal/catalog/") || strings.HasPrefix(source.path, "internal/downstream/") {
			continue
		}
		for _, symbol := range []string{"Routes().Resolve(", ".Acquire(ctx"} {
			if strings.Contains(source.contents, symbol) {
				t.Errorf("%s: prohibited capability consumer %s", source.path, symbol)
			}
		}
	}
}

func gatewayModuleRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(current), "../.."))
}

func productionSources(t *testing.T, root string) []productionSource {
	t.Helper()
	result := make([]productionSource, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			if entry.Name() == ".git" || filepath.ToSlash(relative) == "internal/testutil" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.HasPrefix(string(contents), "//go:build e2e\n") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, contents, 0)
		if parseErr != nil {
			return parseErr
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		imports := make(map[string]string, len(parsed.Imports))
		for _, spec := range parsed.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			name := filepath.Base(imported)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			imports[name] = imported
		}
		result = append(result, productionSource{path: filepath.ToSlash(relative), contents: string(contents), file: parsed, imports: imports})
		return nil
	})
	require.NoError(t, err)
	return result
}

func (source productionSource) selectorPackage(selector *ast.SelectorExpr) string {
	identifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return source.imports[identifier.Name]
}

func readProductionSource(t *testing.T, root, relative string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	require.NoError(t, err)
	return string(contents)
}
