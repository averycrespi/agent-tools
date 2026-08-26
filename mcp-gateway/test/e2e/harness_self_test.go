//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type assertionCapture struct {
	output string
}

func (capture *assertionCapture) Errorf(format string, arguments ...any) {
	capture.output = fmt.Sprintf(format, arguments...)
}

func TestGatewayHarnessReusesOneBuiltBinary(t *testing.T) {
	first := newGatewayHarness(t)
	second := newGatewayHarness(t)
	assert.Equal(t, first.binary, second.binary)
	assert.Equal(t, gatewayBinaryPath, first.binary)
}

func TestGatewayHarnessCleansProcessesAndBoundsTimeoutOutput(t *testing.T) {
	var cleaned *gatewayHarness
	t.Run("cleanup owns an unclosed process", func(t *testing.T) {
		cleaned = newGatewayHarness(t)
		cleaned.Start()
	})
	require.Nil(t, cleaned.process)
	connection, err := net.DialTimeout("tcp", cleaned.authority, 100*time.Millisecond)
	require.Error(t, err)
	if connection != nil {
		require.NoError(t, connection.Close())
	}

	runner, err := testutil.NewBinaryRunner(200*time.Millisecond, 8)
	require.NoError(t, err)
	result, err := runner.Run(context.Background(), "sh", "-c", `printf 0123456789; exec tail -f /dev/null`)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, []byte("01234567"), result.Stdout)
	assert.True(t, result.StdoutTruncated)
}

func TestGatewayHarnessHasOneRawServeOwnerAndNoSDKClient(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(source), "*_test.go"))
	require.NoError(t, err)
	serveOwners := 0
	serveNeedle := "runner.Start(" + "harness.ctx, harness.binary, harness.serveArgs...)"
	sdkNeedle := "modelcontextprotocol/" + "go-sdk"
	execNeedle := "exec." + "Command"
	for _, path := range files {
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		text := string(contents)
		serveOwners += strings.Count(text, serveNeedle)
		assert.NotContains(t, text, sdkNeedle)
		assert.NotContains(t, text, execNeedle)
	}
	assert.Equal(t, 1, serveOwners)
}

func TestGatewayHarnessOwnsPrincipalCatalogAndRawMCPWire(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)

	catalog := harness.SetupCurrentCatalog("harness", []fixtureTool{{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("Harness agent", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	assert.Equal(t, "[redacted agent bearer]", fmt.Sprint(issued.Bearer))
	redacted, err := json.Marshal(issued.Bearer)
	require.NoError(t, err)
	assert.JSONEq(t, `"[redacted agent bearer]"`, string(redacted))
	firstCanary := contract.AgentBearerPrefix + "first-diagnostic-canary"
	secondCanary := contract.AgentBearerPrefix + "second-diagnostic-canary"
	capture := &assertionCapture{}
	assert.False(t, assert.Equal(capture, newAgentBearer(t, firstCanary), newAgentBearer(t, secondCanary)))
	assert.NotContains(t, capture.output, firstCanary)
	assert.NotContains(t, capture.output, secondCanary)

	grant := harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("alpha")})
	assert.Equal(t, grant.ID, harness.GetGrant(grant.ID).ID)
	listed := harness.ListGrants(principal.Resource.ID, catalog.ServerID)
	assert.Contains(t, listed, grant)

	modern := harness.ModernDiscover(issued.Bearer, json.RawMessage(`"modern\\nid"`))
	assert.Equal(t, http.StatusOK, modern.StatusCode)
	assert.Equal(t, `{"jsonrpc":"2.0","id":"modern\\nid","result":{"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"mcp-gateway","version":"s1"}},"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28","2025-11-25","2025-06-18","2025-03-26","2024-11-05"],"capabilities":{"tools":{}}}}`, string(modern.Body))
	modernList := harness.ModernList(issued.Bearer, json.RawMessage(`9007199254740993`), "")
	assert.Equal(t, `{"jsonrpc":"2.0","id":9007199254740993,"result":{"tools":[{"annotations":{"destructiveHint":true,"idempotentHint":false,"openWorldHint":true,"readOnlyHint":false},"inputSchema":{"type":"object"},"name":"harness.alpha"}]}}`, string(modernList.Body))

	session, initialized := harness.LegacyInitialize(issued.Bearer, json.RawMessage(`1`))
	assert.Equal(t, `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2025-11-25","serverInfo":{"name":"mcp-gateway","version":"s1"}}}`, string(initialized.Body))
	legacyList := harness.LegacyList(issued.Bearer, session, json.RawMessage(`"legacy\\nid"`), "")
	assert.Equal(t, `{"jsonrpc":"2.0","id":"legacy\\nid","result":{"tools":[{"annotations":{"destructiveHint":true,"idempotentHint":false,"openWorldHint":true,"readOnlyHint":false},"inputSchema":{"type":"object"},"name":"harness.alpha"}]}}`, string(legacyList.Body))
	deleted := harness.LegacyDelete(issued.Bearer, session)
	assert.Equal(t, http.StatusNoContent, deleted.StatusCode)
	assert.Empty(t, deleted.Body)

	harness.DeleteGrant(grant.ID)
}

func pointerTo[T any](value T) *T { return &value }

func TestGatewayHarnessPublishesDeterministicStaticAuthorityThroughProductionAPI(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)
	events := harness.OpenEvents()
	require.Equal(t, http.StatusOK, events.StatusCode)
	assert.Equal(t, contract.MediaTypeEventStream, events.Header.Get("Content-Type"))
	require.NoError(t, events.Body.Close())
	var created struct {
		Server struct {
			ID                  string                       `json:"id"`
			DesiredRevision     string                       `json:"desired_revision"`
			CredentialRevisions contract.CredentialRevisions `json:"credential_revisions"`
		} `json:"server"`
	}
	response := harness.AdminJSON(http.MethodPost, "/api/v1/servers", `{"namespace":"authority-fixture","display_name":"Authority fixture","enabled":false,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"token"}}}`, map[string]string{"Idempotency-Key": "authority-fixture"}, &created)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	etag := response.Header.Get("ETag")
	require.NoError(t, response.Body.Close())
	require.NotEmpty(t, etag)
	assert.Equal(t, "0", created.Server.CredentialRevisions.StaticCredential)

	canary := "e2e-static-authority-canary"
	var replacement contract.CredentialReplacementResult
	response = harness.AdminJSON(http.MethodPost, "/api/v1/servers/"+created.Server.ID+"/credential-replacements", `{"kind":"static_credential","expected_revision":"0","values":{"token":"`+canary+`"}}`, map[string]string{"If-Match": etag}, &replacement)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	replacementBody, err := json.Marshal(replacement)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "1", replacement.CredentialRevision)
	assert.False(t, bytes.Contains(replacementBody, []byte(canary)))
	harness.WaitOperation(created.Server.ID, replacement.Operation.ID, contract.OperationSucceeded)

	var current struct {
		CredentialRevisions contract.CredentialRevisions `json:"credential_revisions"`
	}
	response = harness.AdminJSON(http.MethodGet, "/api/v1/servers/"+created.Server.ID, "", nil, &current)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "1", current.CredentialRevisions.StaticCredential)
}
