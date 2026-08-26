package composition

import (
	"fmt"
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

func TestProductionRootUsesConcreteNativeCompositionAndPositiveIngress(t *testing.T) {
	root := gatewayModuleRoot(t)
	rootSource := readProductionSource(t, root, "cmd/mcp-gateway/root.go")
	compositionSource := readProductionSource(t, root, "internal/composition/composition.go")
	nativeProviderSource := readProductionSource(t, root, "internal/composition/provider_factory.go")
	e2eProviderSource := readProductionSource(t, root, "internal/composition/provider_factory_e2e.go")
	for _, required := range []string{
		"newComposition: composition.New", "newComposition(composition.Options{", "authorizationRepository := runtime.Authorization()", "Principals:  authorizationRepository", "GrantTarget: func(", "serverRepository.ValidateGrantTargetTx", "activeCatalog := runtime.ActiveCatalog()",
		"ActiveCatalog:  activeCatalog", "agentIngress, ok := runtime.AgentIngress()", "Authenticator: agentIngress.Authenticator", "ListTools:     agentIngress.ListTools", "agentIngress.AuthMode", "runtime.AuthorizationOccupancy(context.Background())", "runtime.Start(ctx)", "runtime.Drain(shutdownCtx)",
	} {
		assert.Contains(t, rootSource, required, "cmd/mcp-gateway/root.go: missing production symbol %s", required)
	}
	for _, prohibited := range []string{"runtimes.New(", "unavailableDriver", "absentCatalog", "newMemoryPublisher", "Routes().Resolve(", "mcpingress.DenyAllAuthenticator{}", "AgentAuth: contract.AgentAuthDenyAll"} {
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
		for _, symbol := range []string{"Routes().Resolve(", ".Acquire(ctx"} {
			if strings.Contains(source.contents, symbol) {
				t.Errorf("%s: prohibited capability consumer %s", source.path, symbol)
			}
		}
	}
}

func TestProductionS3DiscoverySliceGuards(t *testing.T) {
	root := gatewayModuleRoot(t)
	for _, source := range productionSources(t, root) {
		for _, violation := range s3SliceViolations(source) {
			t.Error(violation)
		}
	}
	for _, violation := range s3MigrationViolations(t, root) {
		t.Error(violation)
	}
	compositionSource := readProductionSource(t, root, "internal/composition/composition.go")
	for _, symbol := range []string{
		"type AgentIngressDependencies struct", "Authenticator mcpingress.AgentAuthenticator",
		"ListTools     mcpingress.ToolsListService", "AuthMode:      contract.AgentAuthPrincipalCredentials",
	} {
		assert.Contains(t, compositionSource, symbol, "internal/composition/composition.go: missing atomic production ingress symbol %s", symbol)
	}
	recoverySource := readProductionSource(t, root, "internal/storage/recovery.go")
	assert.Contains(t, recoverySource, "func invalidateAgentCredentialCandidate(")
	assert.Equal(t, 1, strings.Count(recoverySource, "UPDATE principals"), "internal/storage/recovery.go: only stopped candidate recovery may own S3 SQL")
	assert.Equal(t, 1, strings.Count(recoverySource, "principals"), "internal/storage/recovery.go: stopped recovery gets one exact S3 table reference")
	assert.NotContains(t, recoverySource, "authorization_meta")
	assert.NotContains(t, recoverySource, "grants")
}

func TestS3SliceGuardNegativeFixturesReportExactPathAndSymbol(t *testing.T) {
	fixtures := []struct {
		name     string
		path     string
		contents string
		want     string
	}{
		{
			name: "ordinary root deny all", path: "cmd/mcp-gateway/root.go",
			contents: `package main
import "github.com/averycrespi/agent-tools/mcp-gateway/internal/mcpingress"
var _ = mcpingress.DenyAllAuthenticator{}
`,
			want: "cmd/mcp-gateway/root.go: prohibited production ingress symbol mcpingress.DenyAllAuthenticator{}",
		},
		{
			name: "SDK outside ingress", path: "internal/discovery/bad.go",
			contents: `package discovery
import "github.com/modelcontextprotocol/go-sdk/mcp"
var _ = mcp.NewClient
`,
			want: "internal/discovery/bad.go: prohibited SDK import github.com/modelcontextprotocol/go-sdk/mcp",
		},
		{
			name: "duplicate online authority", path: "internal/api/bad.go",
			contents: "package api\nfunc build() { _ = authorization.New(nil, nil, nil) }\n",
			want:     "internal/api/bad.go: prohibited duplicate authority constructor authorization.New(",
		},
		{
			name: "duplicate discovery", path: "internal/api/bad.go",
			contents: "package api\nfunc build() { _ = discovery.New(nil, nil, nil) }\n",
			want:     "internal/api/bad.go: prohibited duplicate discovery constructor discovery.New(",
		},
		{
			name: "S3 SQL outside authorization", path: "internal/api/bad.go",
			contents: "package api\nfunc load() { _ = `SELECT id FROM principals` }\n",
			want:     "internal/api/bad.go: prohibited S3 SQL table principals in function load",
		},
		{
			name: "foreign stopped recovery SQL", path: "internal/storage/recovery.go",
			contents: "package storage\nfunc duplicateRecovery() { _ = `UPDATE principals SET revision = 1` }\n",
			want:     "internal/storage/recovery.go: prohibited S3 SQL table principals in function duplicateRecovery",
		},
		{
			name: "discovery keyring", path: "internal/discovery/bad.go",
			contents: `package discovery
import "github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
var _ keyring.Provider
`,
			want: "internal/discovery/bad.go: prohibited discovery import github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring",
		},
		{
			name: "discovery HTTP", path: "internal/discovery/bad.go",
			contents: `package discovery
import "net/http"
var _ http.Client
`,
			want: "internal/discovery/bad.go: prohibited discovery import net/http",
		},
		{
			name: "discovery control cursor", path: "internal/discovery/bad.go",
			contents: "package discovery\nvar _ SnapshotCursor\n",
			want:     "internal/discovery/bad.go: prohibited discovery symbol SnapshotCursor",
		},
		{
			name: "discovery raw session column", path: "internal/discovery/bad.go",
			contents: "package discovery\nconst query = `SELECT session_id FROM calls`\n",
			want:     "internal/discovery/bad.go: prohibited discovery column session_id",
		},
		{
			name: "capability consumer", path: "internal/api/bad.go",
			contents: "package api\nfunc call() { _, _ = registry.Routes().Resolve(id) }\n",
			want:     "internal/api/bad.go: prohibited capability consumer Routes().Resolve(",
		},
		{
			name: "call slice", path: "internal/api/bad.go",
			contents: "package api\nconst method = `tools/call`\n",
			want:     "internal/api/bad.go: prohibited S4/S5 consumer tools/call",
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			source := parseProductionFixture(t, fixture.path, fixture.contents)
			assert.Contains(t, s3SliceViolations(source), fixture.want)
		})
	}
}

var (
	s3SQLVerb  = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP)\b`)
	s3SQLTable = regexp.MustCompile(`(?i)\b(authorization_meta|principals|grants)\b`)
)

func s3SliceViolations(source productionSource) []string {
	violations := make([]string, 0)
	allowedSDK := source.path == "internal/mcpingress/handler.go" || source.path == "internal/dependencies/dependencies.go"
	for _, imported := range source.imports {
		if strings.HasPrefix(imported, "github.com/modelcontextprotocol/go-sdk/") && !allowedSDK {
			violations = append(violations, fmt.Sprintf("%s: prohibited SDK import %s", source.path, imported))
		}
	}
	if strings.Contains(source.contents, "authorization.New(") {
		allowed := source.path == "internal/composition/composition.go" || source.path == "internal/backup/restore.go"
		if !allowed || strings.Count(source.contents, "authorization.New(") != 1 {
			violations = append(violations, fmt.Sprintf("%s: prohibited duplicate authority constructor authorization.New(", source.path))
		}
	}
	for _, symbol := range []string{"discovery.New(", "discovery.NewCursorCodec(", "discovery.NewPager("} {
		if strings.Contains(source.contents, symbol) && (source.path != "internal/composition/composition.go" || strings.Count(source.contents, symbol) != 1) {
			violations = append(violations, fmt.Sprintf("%s: prohibited duplicate discovery constructor %s", source.path, symbol))
		}
	}
	if source.path == "internal/composition/composition.go" {
		for _, symbol := range []string{
			"type AgentIngressDependencies struct", "func (built *Composition) AgentIngress()",
			"Authenticator: built.authorization", "ListTools:     built.listTools", "AuthMode:      contract.AgentAuthPrincipalCredentials",
		} {
			if strings.Count(source.contents, symbol) != 1 {
				violations = append(violations, fmt.Sprintf("%s: composition ingress symbol %s must occur exactly once", source.path, symbol))
			}
		}
	}
	if source.path == "cmd/mcp-gateway/root.go" {
		for _, symbol := range []string{"mcpingress.DenyAllAuthenticator{}", "AgentAuth: contract.AgentAuthDenyAll"} {
			if strings.Contains(source.contents, symbol) {
				violations = append(violations, fmt.Sprintf("%s: prohibited production ingress symbol %s", source.path, symbol))
			}
		}
		for _, symbol := range []string{
			"runtime.AgentIngress()", "Authenticator: agentIngress.Authenticator",
			"ListTools:     agentIngress.ListTools", "agentIngress.AuthMode",
		} {
			if strings.Count(source.contents, symbol) != 1 {
				violations = append(violations, fmt.Sprintf("%s: production ingress symbol %s must occur exactly once", source.path, symbol))
			}
		}
	}
	violations = append(violations, s3SQLViolations(source)...)
	if strings.HasPrefix(source.path, "internal/discovery/") {
		violations = append(violations, discoveryViolations(source)...)
	}
	if !strings.HasPrefix(source.path, "internal/catalog/") && !strings.HasPrefix(source.path, "internal/downstream/") {
		if strings.Contains(source.contents, "Routes().Resolve(") {
			violations = append(violations, fmt.Sprintf("%s: prohibited capability consumer Routes().Resolve(", source.path))
		}
		ast.Inspect(source.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Acquire" || source.selectorPackage(selector) == "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths" {
				return true
			}
			violations = append(violations, fmt.Sprintf("%s: prohibited capability consumer .Acquire(", source.path))
			return true
		})
	}
	if source.path != "internal/downstream/call.go" && strings.Contains(source.contents, "tools/call") {
		violations = append(violations, fmt.Sprintf("%s: prohibited S4/S5 consumer tools/call", source.path))
	}
	invocationPrivacyPrimitive := source.path == "internal/invocation/redaction.go"
	if strings.HasPrefix(source.path, "internal/audit/") || (strings.HasPrefix(source.path, "internal/invocation/") && !invocationPrivacyPrimitive) || strings.HasPrefix(source.path, "internal/ui/") {
		violations = append(violations, fmt.Sprintf("%s: prohibited S4/S5 package", source.path))
	}
	return violations
}

func s3SQLViolations(source productionSource) []string {
	if strings.HasPrefix(source.path, "internal/authorization/") {
		return nil
	}
	functions := make(map[*ast.BasicLit]string)
	for _, declaration := range source.file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if literal, ok := node.(*ast.BasicLit); ok {
				functions[literal] = function.Name.Name
			}
			return true
		})
	}
	violations := make([]string, 0)
	ast.Inspect(source.file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil || !s3SQLVerb.MatchString(value) {
			return true
		}
		function := functions[literal]
		if source.path == "internal/storage/recovery.go" && function == "invalidateAgentCredentialCandidate" {
			return true
		}
		seen := make(map[string]struct{})
		for _, match := range s3SQLTable.FindAllStringSubmatch(value, -1) {
			table := strings.ToLower(match[1])
			if _, duplicate := seen[table]; duplicate {
				continue
			}
			seen[table] = struct{}{}
			location := "package scope"
			if function != "" {
				location = "function " + function
			}
			violations = append(violations, fmt.Sprintf("%s: prohibited S3 SQL table %s in %s", source.path, table, location))
		}
		return true
	})
	return violations
}

func discoveryViolations(source productionSource) []string {
	violations := make([]string, 0)
	for _, imported := range source.imports {
		prohibited := imported == "os" || strings.HasPrefix(imported, "os/") || imported == "runtime" || imported == "syscall" || imported == "database/sql" || imported == "net" || strings.HasPrefix(imported, "net/") ||
			strings.HasSuffix(imported, "/internal/keyring") || strings.HasSuffix(imported, "/internal/downstream") || strings.HasSuffix(imported, "/internal/runtimes") || strings.HasSuffix(imported, "/internal/remote") || strings.HasSuffix(imported, "/internal/paths") ||
			strings.HasSuffix(imported, "/internal/api") || strings.HasSuffix(imported, "/internal/events") || strings.HasPrefix(imported, "github.com/modelcontextprotocol/go-sdk/")
		if prohibited {
			violations = append(violations, fmt.Sprintf("%s: prohibited discovery import %s", source.path, imported))
		}
	}
	for _, symbol := range []string{
		"Routes().Resolve(", "RouteRegistry", "downstream.Capability", "SnapshotCursor", "DescriptorCursor", "ActiveCursor", "ControlCursor",
		"ClientSession.ListTools", "mcp.NewClient(", "notifications/tools/list_changed", "subscriptions/",
	} {
		if strings.Contains(source.contents, symbol) {
			violations = append(violations, fmt.Sprintf("%s: prohibited discovery symbol %s", source.path, symbol))
		}
	}
	columnPattern := regexp.MustCompile(`(?i)\b(credential_verifier|credential_secret|raw_secret|bearer|access_token|refresh_token|client_secret|session(?:_[a-z][a-z0-9_]*)?|history(?:_[a-z][a-z0-9_]*)?)\b`)
	seenColumns := make(map[string]struct{})
	for _, match := range columnPattern.FindAllStringSubmatch(source.contents, -1) {
		column := strings.ToLower(match[1])
		if _, duplicate := seenColumns[column]; duplicate {
			continue
		}
		seenColumns[column] = struct{}{}
		violations = append(violations, fmt.Sprintf("%s: prohibited discovery column %s", source.path, column))
	}
	return violations
}

func s3MigrationViolations(t *testing.T, root string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "internal/storage/migrations/*.sql"))
	require.NoError(t, err)
	violations := make([]string, 0)
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		if filepath.Base(path) == "008_authorization.sql" || !s3SQLVerb.Match(contents) {
			continue
		}
		relative, relErr := filepath.Rel(root, path)
		require.NoError(t, relErr)
		seen := make(map[string]struct{})
		for _, match := range s3SQLTable.FindAllSubmatch(contents, -1) {
			table := strings.ToLower(string(match[1]))
			if _, duplicate := seen[table]; duplicate {
				continue
			}
			seen[table] = struct{}{}
			violations = append(violations, fmt.Sprintf("%s: prohibited S3 migration table %s", filepath.ToSlash(relative), table))
		}
	}
	migration := readProductionSource(t, root, "internal/storage/migrations/008_authorization.sql")
	for _, table := range []string{"authorization_meta", "principals", "grants"} {
		if !strings.Contains(migration, table) {
			violations = append(violations, fmt.Sprintf("internal/storage/migrations/008_authorization.sql: missing S3 migration table %s", table))
		}
	}
	return violations
}

func parseProductionFixture(t *testing.T, path, contents string) productionSource {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, contents, 0)
	require.NoError(t, err)
	imports := make(map[string]string, len(parsed.Imports))
	for _, spec := range parsed.Imports {
		imported, unquoteErr := strconv.Unquote(spec.Path.Value)
		require.NoError(t, unquoteErr)
		name := filepath.Base(imported)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = imported
	}
	return productionSource{path: path, contents: contents, file: parsed, imports: imports}
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
