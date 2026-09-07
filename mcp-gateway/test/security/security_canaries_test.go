//go:build security

package security

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/averycrespi/agent-tools/mcp-gateway/test/acceptance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDurableSecretSinkBoundaries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, "01ARZ3NDEKTSV4RRFFQ69G5FA0")
	require.NoError(t, err)
	entropy := make([]byte, 4096)
	for index := range entropy {
		entropy[index] = byte(index%251 + 1)
	}
	repository, err := authorization.New(store, securityClock{}, bytes.NewReader(entropy))
	require.NoError(t, err)
	created, err := repository.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{DisplayName: "Security agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	credential, err := repository.IssueCredential(context.Background(), created.Principal.ID, created.Principal.Revision)
	require.NoError(t, err)
	backup := filepath.Join(t.TempDir(), "gateway.db")
	require.NoError(t, store.BackupTo(context.Background(), backup))
	require.NoError(t, store.Close())
	require.NoError(t, ownership.MarkClean())
	require.NoError(t, ownership.Close())

	scanner, err := testutil.NewCanaryScanner([]byte(credential.Bearer))
	require.NoError(t, err)
	for _, path := range append(regularFiles(t, root), backup) {
		file, openErr := os.Open(path)
		require.NoError(t, openErr)
		scanErr := scanner.Scan(path, file)
		closeErr := file.Close()
		require.NoError(t, scanErr)
		require.NoError(t, closeErr)
	}
}

func TestSecurityEvidenceOwnerManifest(t *testing.T) {
	type privacyOwner struct {
		class string
		pkg   string
		owner string
	}
	owners := []privacyOwner{
		{"browser stores", "./test/e2e", "TestBrowserSecretStoragePrivacy"}, {"URL fragments and attributes", "./test/e2e", "TestBrowserSecretStoragePrivacy"},
		{"DOM and input values", "./test/e2e", "TestBrowserSecretStoragePrivacy"}, {"one-time display lifecycle", "./test/e2e", "TestBrowserSecretSinks"},
		{"user-gesture clipboard", "./test/e2e", "TestBrowserSecretSinks"}, {"OAuth opener and referrer", "./test/e2e", "TestBrowserSecretStoragePrivacy"},
		{"stale authentication epoch", "./test/e2e", "TestBrowserSecretSinks"}, {"post-response sink loss", "./test/e2e", "TestBrowserSecretSinks"},
		{"CLI argv and environment", "./cmd/mcp-gateway", "TestCLISensitiveSinks"}, {"CLI stdout and stderr", "./cmd/mcp-gateway", "TestCLISensitiveSinks"},
		{"logs and acceptance reports", "./test/acceptance", "TestReleaseReportSecretSinkBoundaries"}, {"events", "./test/e2e", "TestE2EInvocationReadPrivacy"},
		{"audit capture", "./test/e2e", "TestE2EInvocationReadPrivacy"}, {"SQLite and backups", "./test/security", "TestDurableSecretSinkBoundaries"},
		{"generated frontend assets", "./test/security", "TestSecurityEvidenceOwnerManifest"}, {"screenshots and reports", "./test/e2e", "TestBrowserSecretStoragePrivacy"},
		{"process output", "./test/e2e", "TestE2EInvocationReadPrivacy"}, {"test artifacts", "./test/security", "TestSecurityEvidenceOwnerManifest"},
	}
	require.Len(t, owners, 18)
	moduleRoot := filepath.Join(repositoryRoot(t), "mcp-gateway")
	inventory, err := acceptance.DiscoverSuiteInventory(moduleRoot, runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)
	selected := make(map[string]string)
	for _, test := range inventory.Tests {
		if test.Selected {
			selected[test.Package+"/"+test.Name] = test.Owner
		}
	}
	classes := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		leaf, exists := selected[owner.pkg+"/"+owner.owner]
		require.True(t, exists, "%s has no selected executable owner", owner.class)
		_, err := acceptance.PlanSuite(moduleRoot, leaf, inventory, 1)
		require.NoError(t, err, owner.class)
		_, duplicate := classes[owner.class]
		assert.False(t, duplicate, owner.class)
		classes[owner.class] = struct{}{}
	}

	root := repositoryRoot(t)
	webRoot := filepath.Join(root, "mcp-gateway", "web", "src")
	var localStorageUses int
	require.NoError(t, filepath.Walk(webRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || (filepath.Ext(path) != ".ts" && filepath.Ext(path) != ".tsx") {
			return walkErr
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(contents)
		for _, forbidden := range []string{"sessionStorage", "indexedDB", "serviceWorker.register", "console.log", "console.debug", "document.cookie =", "dangerouslySetInnerHTML"} {
			assert.NotContains(t, text, forbidden, path)
		}
		uses := strings.Count(text, "localStorage.")
		if uses > 0 {
			assert.Equal(t, "theme.ts", filepath.Base(path), path)
			localStorageUses += uses
		}
		return nil
	}))
	assert.Equal(t, 3, localStorageUses, "only the closed theme preference may use browser storage")

	staticRoot := filepath.Join(root, "mcp-gateway", "internal", "api", "static")
	entries, err := os.ReadDir(staticRoot)
	require.NoError(t, err)
	assert.Equal(t, []string{"app.css", "app.js", "favicon.svg", "index.html"}, func() []string {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
			assert.False(t, strings.HasSuffix(entry.Name(), ".map"), entry.Name())
		}
		sort.Strings(names)
		return names
	}())
	canary := []byte("frontend-artifact-" + "privacy-canary-4f71ac")
	scanner, err := testutil.NewCanaryScanner(canary)
	require.NoError(t, err)
	for _, path := range regularFiles(t, staticRoot) {
		file, openErr := os.Open(path)
		require.NoError(t, openErr)
		scanErr := scanner.Scan(path, file)
		closeErr := file.Close()
		require.NoError(t, scanErr)
		require.NoError(t, closeErr)
	}
}

func TestStaticSecretSinkClosure(t *testing.T) {
	assert.Equal(t, []contract.SecretSink{
		contract.SecretSinkControllingTerminal, contract.SecretSinkOwnerOnlyFile, contract.SecretSinkAdminCredentialReplacement,
		contract.SecretSinkDCRClientSecret, contract.SecretSinkAuthorizationCodeTokenResponse, contract.SecretSinkRefreshResponse,
		contract.SecretSinkAuthoritativeGenerationRefreshCopy, contract.SecretSinkAgentCredentialCreation,
		contract.SecretSinkBrowserOneTimeDisplay, contract.SecretSinkUserInitiatedClipboard,
	}, contract.ApprovedSecretSinks())
	reportSchema, err := os.ReadFile(filepath.Join(repositoryRoot(t), "mcp-gateway", "test", "acceptance", "release_report.schema.json"))
	require.NoError(t, err)
	for _, forbidden := range []string{`"stdout"`, `"stderr"`, `"error"`, `"output"`} {
		assert.NotContains(t, string(reportSchema), forbidden)
	}
	assert.Equal(t, []string{"AdmissionClass", "AdmittedAt", "AuthorizationDecision", "AuthorizationRevision", "CompletedAt", "CredentialFingerprint", "CredentialID", "CredentialRevision", "DescriptorFingerprint", "DescriptorRevision", "EvaluatedAt", "GrantID", "InvocationID", "PrincipalID", "RedactedArguments", "RequestedName", "Sequence", "ServerID", "TerminalClass", "ToolID", "UpstreamName"}, exportedFields(reflect.TypeOf(contract.InvocationAuditRecord{})))

	assert.Equal(t, []string{"Action", "Actor", "Category", "CorrelationID", "ID", "Initiator", "Outcome", "Phase", "Sequence", "Target", "Timestamp"}, exportedFields(reflect.TypeOf(contract.AuditSummary{})))
	assert.Equal(t, []string{"AuditSummary", "Detail"}, exportedFields(reflect.TypeOf(contract.AuditEvent{})))
	assert.Equal(t, []string{"Problem", "Reason"}, exportedFields(reflect.TypeOf(contract.AuditDetail{})))
	assert.Equal(t, []string{"Credential", "Type"}, exportedFields(reflect.TypeOf(contract.AuditActor{})))
	assert.Equal(t, []string{"Fingerprint", "ID"}, exportedFields(reflect.TypeOf(contract.AuditCredential{})))
	for _, pattern := range []string{"/api/v1/audit-events", "/api/v1/audit-events/{id}"} {
		route, found := contract.RouteForPath(pattern)
		require.True(t, found)
		assert.Equal(t, contract.AuthorityAdmin, route.Authority)
		assert.Equal(t, []string{"GET"}, route.Methods)
	}

	for _, base := range []string{"cmd", "internal"} {
		require.NoError(t, filepath.Walk(filepath.Join(repositoryRoot(t), "mcp-gateway", base), func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || strings.Contains(path, string(filepath.Separator)+"testutil"+string(filepath.Separator)) {
				return walkErr
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(contents), "/api/v1/audit") {
				allowed := []string{"internal/api/handler.go", "internal/contract/resources.go", "internal/contract/routes.go"}
				assert.Contains(t, allowed, filepath.ToSlash(strings.TrimPrefix(path, filepath.Join(repositoryRoot(t), "mcp-gateway")+string(filepath.Separator))), path)
			}
			if strings.Contains(string(contents), "/api/v1/invocations") {
				allowed := []string{"cmd/mcp-gateway/online_reads.go", "internal/api/handler.go", "internal/contract/resources.go", "internal/contract/routes.go"}
				assert.Contains(t, allowed, filepath.ToSlash(strings.TrimPrefix(path, filepath.Join(repositoryRoot(t), "mcp-gateway")+string(filepath.Separator))), path)
			}
			parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, contents, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			for _, imported := range parsed.Imports {
				assert.NotContains(t, []string{`"log"`, `"log/slog"`}, imported.Path.Value, path)
			}
			return nil
		}))
	}
}

type securityClock struct{}

func (securityClock) Now() time.Time {
	return time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func regularFiles(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr == nil && !info.IsDir() {
			paths = append(paths, path)
		}
		return walkErr
	}))
	return paths
}

func exportedFields(value reflect.Type) []string {
	fields := make([]string, 0, value.NumField())
	for index := range value.NumField() {
		field := value.Field(index)
		if ast.IsExported(field.Name) {
			fields = append(fields, field.Name)
		}
	}
	sort.Strings(fields)
	return fields
}
