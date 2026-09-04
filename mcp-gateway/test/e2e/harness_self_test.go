//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		if filepath.Base(path) != "frontend_development_test.go" {
			assert.NotContains(t, text, execNeedle)
		}
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
	assertDiscoveryNamePage(t, modernList, withSyntheticNames([]string{"harness.alpha"}), "")

	session, initialized := harness.LegacyInitialize(issued.Bearer, json.RawMessage(`1`))
	assert.Equal(t, `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2025-11-25","serverInfo":{"name":"mcp-gateway","version":"s1"}}}`, string(initialized.Body))
	legacyList := harness.LegacyList(issued.Bearer, session, json.RawMessage(`"legacy\\nid"`), "")
	assertDiscoveryNamePage(t, legacyList, withSyntheticNames([]string{"harness.alpha"}), "")
	deleted := harness.LegacyDelete(issued.Bearer, session)
	assert.Equal(t, http.StatusNoContent, deleted.StatusCode)
	assert.Empty(t, deleted.Body)

	harness.DeleteGrant(grant.ID)
}

func pointerTo[T any](value T) *T { return &value }

func TestCallFixturesExposeOnlyClosedSafeObservations(t *testing.T) {
	fixture := newRawHTTPFixture(t, "modern")
	call := func(outcome fixtureCallOutcome) (*http.Response, error) {
		fixture.SetCallOutcome(outcome)
		return http.Post(fixture.URL(), contract.MediaTypeJSON, strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"alpha","arguments":{"value":"fixture-request-canary"}}}`))
	}
	for _, test := range []struct {
		outcome fixtureCallOutcome
		body    string
	}{
		{fixtureCallSuccess, `{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"fixture success"}]}}`},
		{fixtureCallToolError, `{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"fixture private tool error"}],"isError":true}}`},
		{fixtureCallMalformed, `{"jsonrpc":"2.0","id":7,"result":{"content":"malformed"}}`},
	} {
		response, err := call(test.outcome)
		require.NoError(t, err)
		assert.JSONEq(t, test.body, string(readResponseBody(t, response)))
	}
	_, err := call(fixtureCallUncertain)
	require.Error(t, err)

	fixture.SetCallOutcome(fixtureCallSuccess)
	barrier := fixture.Arm("tools/call")
	completed := make(chan error, 1)
	go func() {
		response, requestErr := http.Post(fixture.URL(), contract.MediaTypeJSON, strings.NewReader(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"alpha","arguments":{}}}`))
		if requestErr == nil {
			_, requestErr = io.Copy(io.Discard, response.Body)
			requestErr = errors.Join(requestErr, response.Body.Close())
		}
		completed <- requestErr
	}()
	awaitFixtureSignal(t, barrier.entered, "call fixture barrier was not entered")
	barrier.Release()
	require.NoError(t, <-completed)
	awaitFixtureSignal(t, barrier.completed, "call fixture barrier did not complete")

	evidence, err := json.Marshal(fixture.Events())
	require.NoError(t, err)
	assert.NotContains(t, string(evidence), "fixture-request-canary")
	assert.NotContains(t, string(evidence), fixtureSuccessText)
	assert.NotContains(t, string(evidence), fixtureToolErrorText)
	assert.Len(t, fixture.Events(), 5)
	assert.Equal(t, fixtureCallSuccess, fixtureCallOutcomeForMode("modern"))
	assert.Equal(t, fixtureCallToolError, fixtureCallOutcomeForMode("call-tool-error"))
	assert.Equal(t, fixtureCallMalformed, fixtureCallOutcomeForMode("call-malformed"))
	assert.Equal(t, fixtureCallUncertain, fixtureCallOutcomeForMode("call-uncertain"))
}

func TestGatewayHarnessObservesGovernedCallsWithoutRetainingPayloads(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()

	catalog := harness.SetupCurrentCatalog("call-harness", []fixtureTool{{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("Call harness agent", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("alpha")})

	catalogBarrier := catalog.Fixture.Arm("tools/list")
	refresh := createServerOperation(t, harness, catalog.ServerID, catalog.ETag, string(contract.OperationRefreshCatalog), "call-harness-refresh")
	awaitFixtureSignal(t, catalogBarrier.entered, "catalog refresh barrier was not entered")

	const argumentCanary = "e2e-call-argument-canary"
	catalog.Fixture.SetCallOutcome(fixtureCallSuccess)
	modern := harness.ModernCall(issued.Bearer, json.RawMessage(`"modern-call"`), "call-harness.alpha", json.RawMessage(`{"value":"`+argumentCanary+`"}`))
	assert.Equal(t, http.StatusOK, modern.StatusCode)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"modern-call","result":{"content":[{"type":"text","text":"fixture success"}]}}`, string(modern.Body))

	catalog.Fixture.SetCallOutcome(fixtureCallToolError)
	session, _ := harness.LegacyInitialize(issued.Bearer, json.RawMessage(`1`))
	legacy := harness.LegacyCall(issued.Bearer, session, json.RawMessage(`"legacy-call"`), "call-harness.alpha", json.RawMessage(`{}`))
	assert.Equal(t, http.StatusOK, legacy.StatusCode)
	var legacyError struct {
		Error struct {
			Code int                         `json:"code"`
			Data contract.AgentCallErrorData `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(legacy.Body, &legacyError))
	assert.Equal(t, contract.AgentCallJSONRPCErrorCode, legacyError.Error.Code)
	assert.Equal(t, contract.DownstreamFailure, legacyError.Error.Data.Code)

	catalogBarrier.Release()
	harness.WaitOperation(catalog.ServerID, refresh.ID, contract.OperationSucceeded)

	result := harness.Stop(syscall.SIGTERM)
	observations := harness.AuditObservations()
	require.Len(t, observations, 2)
	assert.Equal(t, contract.TerminalSucceeded, observations[0].TerminalClass)
	assert.Equal(t, contract.TerminalDownstreamFailure, observations[1].TerminalClass)
	for _, observation := range observations {
		assert.Equal(t, contract.AdmissionEvaluated, observation.AdmissionClass)
		assert.Equal(t, contract.DecisionAllow, observation.Decision)
	}
	fixtureEvidence, err := json.Marshal(catalog.Fixture.Events())
	require.NoError(t, err)
	assert.NotContains(t, string(fixtureEvidence), argumentCanary)
	assert.NotContains(t, string(fixtureEvidence), fixtureToolErrorText)
	assert.NotContains(t, string(result.Stdout), argumentCanary)
	assert.NotContains(t, string(result.Stderr), argumentCanary)
}

func TestGatewayHarnessStdioCallFixtureRecordsOnlySafeFacts(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	executable, err := os.Executable()
	require.NoError(t, err)
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "events.jsonl")
	creation := createStdioServer(t, harness, executable, "call-success", filepath.Join(directory, "marker"), eventsPath)
	harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, creation.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveState == contract.ActiveCatalogCurrent
	})
	principal := harness.CreatePrincipal("Stdio call harness", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: creation.Server.ID, UpstreamName: pointerTo("alpha")})

	const argumentCanary = "stdio-call-argument-canary"
	response := harness.ModernCall(issued.Bearer, json.RawMessage(`9`), "stdio-call-success.alpha", json.RawMessage(`{"value":"`+argumentCanary+`"}`))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":9,"result":{"content":[{"type":"text","text":"fixture success"}]}}`, string(response.Body))
	events := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "request", "tools/call") == 1
	})
	pid := fixtureEvents(events, "start", "")[0].PID
	harness.Stop(syscall.SIGTERM)
	waitForProcessExit(t, pid)
	observations := harness.AuditObservations()
	require.Len(t, observations, 1)
	assert.Equal(t, contract.TerminalSucceeded, observations[0].TerminalClass)
	contents, err := os.ReadFile(eventsPath)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(contents), stdioFixtureObservationLimit)
	assert.NotContains(t, string(contents), argumentCanary)
	assert.NotContains(t, string(contents), fixtureSuccessText)
}

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
