//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestTypeSafeValidationEvidence(t *testing.T) {
	const canary = "PRIVATE-TYPESAFE-VALUE-KEY-CANARY"
	var attempts atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-only" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"models":[{}, {"name":7,"description":"`+canary+`","release_date":"`+canary+`","`+canary+`":true}]}`)
	}))
	t.Cleanup(fixture.Close)
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	module := filepath.Clean(filepath.Join(filepath.Dir(source), "../../../typesafe-mcp"))
	clientSource := filepath.Join(module, "internal/provider/client.go")
	raw, err := os.ReadFile(clientSource)
	require.NoError(t, err)
	require.Contains(t, string(raw), `const origin = "https://api.typesafe.ai"`)
	temp := t.TempDir()
	overlaySource := filepath.Join(temp, "client.go")
	require.NoError(t, os.WriteFile(overlaySource, []byte(strings.Replace(string(raw), `const origin = "https://api.typesafe.ai"`, "const origin = "+strconv.Quote(fixture.URL), 1)), 0600))
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{clientSource: overlaySource}})
	require.NoError(t, err)
	overlayPath := filepath.Join(temp, "overlay.json")
	require.NoError(t, os.WriteFile(overlayPath, overlay, 0600))
	binary := filepath.Join(temp, "typesafe-mcp")
	runner, err := testutil.NewBinaryRunner(120*time.Second, 64<<10)
	require.NoError(t, err)
	built, err := runner.RunInDir(t.Context(), module, "go", "build", "-overlay", overlayPath, "-o", binary, "./cmd/typesafe-mcp")
	require.NoError(t, err, "%s", built.Stderr)

	harness := newGatewayHarness(t)
	harness.Start()
	request, err := json.Marshal(map[string]any{
		"namespace": "typesafe", "display_name": "TypeSafe fixture", "enabled": false,
		"transport": map[string]any{"kind": "stdio", "executable": binary, "arguments": []string{}, "working_directory": temp, "environment": map[string]string{}, "secret_environment": map[string]string{"TYPESAFE_API_KEY": "token"}},
	})
	require.NoError(t, err)
	var creation stdioCreation
	response := harness.AdminJSON(http.MethodPost, "/api/v2/mcp/servers", string(request), map[string]string{"Idempotency-Key": "typesafe-validation"}, &creation)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	etag := response.Header.Get("ETag")
	require.NoError(t, response.Body.Close())
	var replacement contract.CredentialReplacementResult
	response = harness.AdminJSON(http.MethodPost, "/api/v2/mcp/servers/"+creation.Server.ID+"/credential-replacements", `{"kind":"static_credential","expected_revision":"0","values":{"token":"fixture-only"}}`, map[string]string{"If-Match": etag}, &replacement)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	require.NoError(t, response.Body.Close())
	harness.WaitOperation(creation.Server.ID, replacement.Operation.ID, contract.OperationSucceeded)
	harness.WaitSettledOperation(creation.Server.ID, replacement.Operation.ID)
	enabled, _ := patchServer(t, harness, creation.Server.ID, etag, `{"enabled":true}`)
	require.NotNil(t, enabled.Operation)
	harness.WaitOperation(creation.Server.ID, enabled.Operation.ID, contract.OperationSucceeded)
	harness.WaitSettledOperation(creation.Server.ID, enabled.Operation.ID)
	waitForStdioServer(t, harness, creation.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveToolCount == 2
	})
	principal := harness.CreatePrincipal("TypeSafe caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: creation.Server.ID, UpstreamName: pointerTo("list_models")})
	call := harness.ModernCall(issued.Bearer, json.RawMessage(`"validation"`), "typesafe.list_models", json.RawMessage(`{}`))
	id := assertCallError(t, call, json.RawMessage(`"validation"`), contract.DownstreamFailure, false)
	require.EqualValues(t, 1, attempts.Load())
	require.NotContains(t, string(call.Body), canary)
	var live struct {
		Error struct {
			Data contract.AgentCallErrorData `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(call.Body, &live))
	require.NotNil(t, live.Error.Data.Diagnostics.ServerReported)
	details := live.Error.Data.Diagnostics.ServerReported.Validation
	require.NotNil(t, details)
	require.True(t, details.Truncated)
	require.GreaterOrEqual(t, len(details.Violations), 2)
	harness.Restart()
	item := harness.adminSnapshot(http.MethodGet, "/api/v2/mcp/invocations/"+id, nil)
	require.Equal(t, http.StatusOK, item.StatusCode)
	var retained contract.Invocation
	require.NoError(t, json.Unmarshal(item.Body, &retained))
	require.Equal(t, live.Error.Data.Diagnostics, retained.Diagnostics)
	require.NotContains(t, string(item.Body), canary)
	bearerPath := filepath.Join(temp, "administrator")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer), 0600))
	cli := runOnlineCLI(t, harness, bearerPath, true, "mcp", "invocation", "get", id, "--output", "json")
	require.JSONEq(t, string(item.Body), string(cli.Stdout))
	human := runOnlineCLI(t, harness, bearerPath, true, "mcp", "invocation", "get", id)
	require.Contains(t, string(human.Stdout), "Response validation (unverified)")
	require.Contains(t, string(human.Stdout), "Additional validation violations omitted")
	require.NotContains(t, string(human.Stdout)+string(human.Stderr), canary)
	harness.Stop(os.Interrupt)
	for _, result := range harness.results {
		require.NotContains(t, string(result.Stdout)+string(result.Stderr), canary)
	}
}
