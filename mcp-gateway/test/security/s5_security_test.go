//go:build security

package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestS5SecurityAcceptanceReportSinks(t *testing.T) {
	canary := "s5-report-output-canary-7f31289c"
	report := acceptance.Run(context.Background(), repositoryRoot(t), securityExecutor{canary: canary}, true)
	require.Equal(t, acceptance.ResultFailed, report.Result)
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), canary)
	for _, forbidden := range []string{`"stdout"`, `"stderr"`, `"error"`, `"output"`} {
		assert.NotContains(t, string(encoded), forbidden)
	}
}

func TestS5SecurityDurableSinkCanaries(t *testing.T) {
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

func TestS6SecurityCanaries(t *testing.T) {
	type privacyOwner struct {
		class string
		owner string
	}
	owners := []privacyOwner{
		{"browser stores", "assertSecretAbsent"}, {"URL fragments and attributes", "TestS6BrowserPrivacyCanary"},
		{"DOM and input values", "TestS6BrowserPrivacyCanary"}, {"one-time display lifecycle", "assertSensitiveSinkFoundation"},
		{"user-gesture clipboard", "runSecretSinks"}, {"OAuth opener and referrer", "TestS6BrowserPrivacyCanary"},
		{"stale authentication epoch", "assertSensitiveSinkFoundation"}, {"post-response sink loss", "assertSensitiveSinkFoundation"},
		{"CLI argv and environment", "TestCLISensitiveSinks"}, {"CLI stdout and stderr", "TestCLISensitiveSinks"},
		{"logs and acceptance reports", "TestS5SecurityAcceptanceReportSinks"}, {"events", "TestE2EInvocationReadPrivacy"},
		{"audit capture", "TestE2EInvocationReadPrivacy"}, {"SQLite and backups", "TestS5SecurityDurableSinkCanaries"},
		{"generated frontend assets", "TestS6SecurityCanaries"}, {"screenshots and reports", "TestS6BrowserPrivacyCanary"},
		{"process output", "TestE2EInvocationReadPrivacy"}, {"test artifacts", "TestS6SecurityCanaries"},
	}
	require.Len(t, owners, 18)
	classes := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		assert.NotEmpty(t, owner.owner)
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
	assert.Equal(t, []string{"app.css", "app.js", "index.html"}, func() []string {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
			assert.False(t, strings.HasSuffix(entry.Name(), ".map"), entry.Name())
		}
		sort.Strings(names)
		return names
	}())
	canary := []byte("s6-artifact-" + "privacy-canary-4f71ac")
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

func TestS5SecurityStaticSinkClosure(t *testing.T) {
	assert.Equal(t, []contract.SecretSink{
		contract.SecretSinkControllingTerminal, contract.SecretSinkOwnerOnlyFile, contract.SecretSinkAdminCredentialReplacement,
		contract.SecretSinkDCRClientSecret, contract.SecretSinkAuthorizationCodeTokenResponse, contract.SecretSinkRefreshResponse,
		contract.SecretSinkAuthoritativeGenerationRefreshCopy, contract.SecretSinkAgentCredentialCreation,
		contract.SecretSinkBrowserOneTimeDisplay, contract.SecretSinkUserInitiatedClipboard,
	}, contract.ApprovedSecretSinks())
	assert.Equal(t, []string{"Artifacts", "Cleanup", "Command", "Criteria", "DiagnosticPaths", "DurationMillis", "EndedAt", "Evidence", "Name", "StartedAt", "Status", "Termination", "TimedOut", "TimeoutMillis"}, exportedFields(reflect.TypeOf(acceptance.Check{})))
	assert.Equal(t, []string{"AdmissionClass", "AdmittedAt", "AuthorizationDecision", "AuthorizationRevision", "CompletedAt", "CredentialFingerprint", "CredentialID", "CredentialRevision", "DescriptorFingerprint", "DescriptorRevision", "EvaluatedAt", "GrantID", "InvocationID", "PrincipalID", "RedactedArguments", "RequestedName", "Sequence", "ServerID", "TerminalClass", "ToolID", "UpstreamName"}, exportedFields(reflect.TypeOf(contract.InvocationAuditRecord{})))

	for _, base := range []string{"cmd", "internal"} {
		require.NoError(t, filepath.Walk(filepath.Join(repositoryRoot(t), "mcp-gateway", base), func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || strings.Contains(path, string(filepath.Separator)+"testutil"+string(filepath.Separator)) {
				return walkErr
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			assert.NotContains(t, string(contents), "/api/v1/audit", path)
			if strings.Contains(string(contents), "/api/v1/invocations") {
				allowed := []string{"internal/api/handler.go", "internal/contract/resources.go", "internal/contract/routes.go"}
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

type securityExecutor struct {
	canary string
}

func (executor securityExecutor) Run(_ context.Context, _ string, _ acceptance.Command) ([]byte, error) {
	return []byte(executor.canary), errors.New(executor.canary)
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
