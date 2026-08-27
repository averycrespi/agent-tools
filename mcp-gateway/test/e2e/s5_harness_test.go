//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type grantRequestHandle struct {
	Resource contract.GrantRequest
	ETag     string
}

func (harness *gatewayHarness) ModernSelfServiceCall(bearer *agentBearer, id json.RawMessage, upstream string, arguments any) responseSnapshot {
	harness.t.Helper()
	return harness.ModernCall(bearer, id, selfServiceToolName(harness.t, upstream), marshalHarnessJSON(harness.t, arguments))
}

func (harness *gatewayHarness) LegacySelfServiceCall(bearer *agentBearer, session legacySessionHandle, id json.RawMessage, upstream string, arguments any) responseSnapshot {
	harness.t.Helper()
	return harness.LegacyCall(bearer, session, id, selfServiceToolName(harness.t, upstream), marshalHarnessJSON(harness.t, arguments))
}

func selfServiceToolName(t *testing.T, upstream string) string {
	t.Helper()
	for _, tool := range contract.SyntheticSelfServiceTools() {
		if tool.UpstreamName == upstream {
			return tool.ExternalName
		}
	}
	t.Fatalf("unknown self-service tool %q", upstream)
	return ""
}

func marshalHarnessJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	contents, err := json.Marshal(value)
	require.NoError(t, err)
	require.LessOrEqual(t, len(contents), harnessResponseLimit)
	return contents
}

func decodeSelfServiceResult[T any](t *testing.T, response responseSnapshot, id json.RawMessage, summary string) T {
	t.Helper()
	require.Equal(t, http.StatusOK, response.StatusCode, string(response.Body))
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Structured T `json:"structuredContent"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &envelope))
	assert.Equal(t, "2.0", envelope.JSONRPC)
	assert.JSONEq(t, string(id), string(envelope.ID))
	require.Len(t, envelope.Result.Content, 1, string(response.Body))
	assert.Equal(t, "text", envelope.Result.Content[0].Type)
	assert.Equal(t, summary, envelope.Result.Content[0].Text)
	return envelope.Result.Structured
}

func (harness *gatewayHarness) ListGrantRequests(principalID string) []contract.GrantRequestSummary {
	harness.t.Helper()
	query := url.Values{"limit": {"100"}}
	if principalID != "" {
		query.Set("principal_id", principalID)
	}
	response := harness.adminSnapshot(http.MethodGet, "/api/v1/grant-requests?"+query.Encode(), nil)
	var page contract.Collection[contract.GrantRequestSummary]
	decodeSnapshot(harness.t, response, http.StatusOK, &page)
	require.Nil(harness.t, page.NextCursor)
	return page.Items
}

func (harness *gatewayHarness) GetGrantRequest(requestID string) grantRequestHandle {
	harness.t.Helper()
	response := harness.adminSnapshot(http.MethodGet, "/api/v1/grant-requests/"+url.PathEscape(requestID), nil)
	var request contract.GrantRequest
	decodeSnapshot(harness.t, response, http.StatusOK, &request)
	return checkedGrantRequest(harness.t, request, response.Header.Get("ETag"))
}

func (harness *gatewayHarness) ApproveGrantRequest(current grantRequestHandle, policy contract.Policy) grantRequestHandle {
	harness.t.Helper()
	body := marshalHarnessJSON(harness.t, contract.GrantRequestApproval{ApprovedPolicy: policy})
	response := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/grant-requests/"+url.PathEscape(current.Resource.ID)+"/approve", body, map[string]string{"If-Match": current.ETag})
	var request contract.GrantRequest
	decodeSnapshot(harness.t, response, http.StatusOK, &request)
	return checkedGrantRequest(harness.t, request, response.Header.Get("ETag"))
}

func (harness *gatewayHarness) RejectGrantRequest(current grantRequestHandle, reason contract.GrantRequestRejectionReason) grantRequestHandle {
	harness.t.Helper()
	body := marshalHarnessJSON(harness.t, contract.GrantRequestRejection{Reason: reason})
	response := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/grant-requests/"+url.PathEscape(current.Resource.ID)+"/reject", body, map[string]string{"If-Match": current.ETag})
	var request contract.GrantRequest
	decodeSnapshot(harness.t, response, http.StatusOK, &request)
	return checkedGrantRequest(harness.t, request, response.Header.Get("ETag"))
}

func checkedGrantRequest(t *testing.T, request contract.GrantRequest, etag string) grantRequestHandle {
	t.Helper()
	require.Equal(t, contract.GrantRequestETag(request.ID, request.Revision), etag)
	return grantRequestHandle{Resource: request, ETag: etag}
}

func (harness *gatewayHarness) GetOnlyDescriptor(serverID string) contract.ToolDescriptor {
	harness.t.Helper()
	response := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+url.PathEscape(serverID)+"/descriptors?limit=2&retired=include", nil)
	var page contract.Collection[contract.ToolDescriptor]
	decodeSnapshot(harness.t, response, http.StatusOK, &page)
	require.Len(harness.t, page.Items, 1)
	return page.Items[0]
}

func assertDescriptorEvidenceMatches(t *testing.T, evidence *contract.DescriptorEvidence, descriptor contract.ToolDescriptor, namespace string) {
	t.Helper()
	require.NotNil(t, evidence)
	assert.Equal(t, descriptor.ID, evidence.ToolID)
	assert.Equal(t, descriptor.ServerID, evidence.ServerID)
	assert.Equal(t, namespace, evidence.Namespace)
	assert.Equal(t, descriptor.UpstreamName, evidence.UpstreamName)
	assert.Equal(t, descriptor.ExternalName, evidence.ExternalName)
	assert.Equal(t, descriptor.CatalogRevision, evidence.CatalogRevision)
	assert.Equal(t, descriptor.Fingerprint, evidence.Fingerprint)
	assert.Equal(t, descriptor.Descriptor, evidence.Descriptor)
}

func (harness *gatewayHarness) AuditCheckpoint() int64 {
	harness.t.Helper()
	observations := harness.LiveAuditObservations()
	if len(observations) == 0 {
		return 0
	}
	return observations[len(observations)-1].Sequence
}

func (harness *gatewayHarness) WaitAuditAfter(sequence int64) []auditObservation {
	harness.t.Helper()
	var delta []auditObservation
	require.Eventually(harness.t, func() bool {
		delta = delta[:0]
		for _, observation := range harness.LiveAuditObservations() {
			if observation.Sequence > sequence {
				delta = append(delta, observation)
			}
		}
		return len(delta) > 0
	}, 3*time.Second, 10*time.Millisecond)
	return delta
}

func (handle currentCatalogHandle) CallCount() int {
	count := 0
	for _, event := range handle.Fixture.Events() {
		if event.Method == "tools/call" {
			count++
		}
	}
	return count
}

func (harness *gatewayHarness) ModernCallDiscardResponse(bearer *agentBearer, id json.RawMessage, name string, arguments json.RawMessage) <-chan error {
	harness.t.Helper()
	connection, err := net.DialTimeout("tcp", harness.authority, time.Second)
	require.NoError(harness.t, err)
	body := rawRPCBody(harness.t, id, "tools/call", callParams(harness.t, name, arguments, `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`))
	request, err := http.NewRequest(http.MethodPost, "http://"+harness.authority+"/mcp", bytes.NewReader(body))
	require.NoError(harness.t, err)
	request.Header.Set("Authorization", bearer.authorizationHeader())
	request.Header.Set("Content-Type", contract.MediaTypeJSON)
	request.Header.Set("Accept", contract.MediaTypeJSON+", "+contract.MediaTypeEventStream)
	request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	require.NoError(harness.t, request.Write(connection))
	done := make(chan error, 1)
	go func() {
		response, readErr := http.ReadResponse(bufio.NewReader(connection), request)
		if readErr == nil {
			_, readErr = response.Body.Read(make([]byte, 1))
			closeErr := response.Body.Close()
			if readErr == nil {
				readErr = closeErr
			}
		}
		closeErr := connection.Close()
		if readErr == nil {
			readErr = closeErr
		}
		done <- readErr
	}()
	return done
}

func (harness *gatewayHarness) RestoreBackup(backupID string) testutil.ProcessResult {
	harness.t.Helper()
	require.Nil(harness.t, harness.process, "restore requires a stopped Gateway")
	secretPath := filepath.Join(harness.t.TempDir(), "restored-admin")
	result, err := harness.runner.Run(harness.ctx, harness.binary, "restore", backupID, "--data-dir", harness.root, "--secret-output", secretPath)
	require.NoError(harness.t, err, string(result.Stderr))
	require.False(harness.t, result.StdoutTruncated)
	require.False(harness.t, result.StderrTruncated)
	harness.results = append(harness.results, result)
	harness.bearer = readBearer(harness.t, secretPath)
	return result
}

func (harness *gatewayHarness) AssertCanaryAbsent(canary []byte) {
	harness.t.Helper()
	scanner, err := testutil.NewCanaryScanner(canary)
	require.NoError(harness.t, err)
	for index, result := range append([]testutil.ProcessResult{harness.initialization}, harness.results...) {
		require.NoError(harness.t, scanner.Scan(fmt.Sprintf("process-%d-stdout", index), bytes.NewReader(result.Stdout)))
		require.NoError(harness.t, scanner.Scan(fmt.Sprintf("process-%d-stderr", index), bytes.NewReader(result.Stderr)))
	}
	require.NoError(harness.t, filepath.Walk(harness.root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		scanErr := scanner.Scan(strings.TrimPrefix(path, harness.root), file)
		closeErr := file.Close()
		if scanErr != nil {
			return scanErr
		}
		return closeErr
	}))
}

func TestS5E2EHarness(t *testing.T) {
	t.Run("term ignoring fixture cleanup", func(t *testing.T) {
		runner, err := testutil.NewBinaryRunner(3*time.Second, 1024)
		require.NoError(t, err)
		process, err := runner.Start(t.Context(), "sh", "-c", `trap '' TERM; printf ready; exec tail -f /dev/null`)
		require.NoError(t, err)
		<-process.StdoutReady()
		require.NoError(t, process.Stop())
		result, waitErr := process.Wait()
		require.Error(t, waitErr)
		assert.True(t, result.Cleanup.KillSent)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	})

	var cleaned *gatewayHarness
	t.Run("injected cleanup", func(t *testing.T) {
		cleaned = newGatewayHarness(t)
		cleaned.Start()
	})
	require.Nil(t, cleaned.process)
	connection, err := net.DialTimeout("tcp", cleaned.authority, 50*time.Millisecond)
	if connection != nil {
		_ = connection.Close()
	}
	require.Error(t, err, "harness cleanup left its listener alive")
	require.NotEmpty(t, cleaned.results)
	assert.False(t, cleaned.results[len(cleaned.results)-1].Cleanup.Survived)

	harness := newGatewayHarness(t)
	harness.Start()
	catalog := harness.SetupCurrentCatalog("s5-harness", []fixtureTool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("S5 harness agent", contract.VisibilityRequestable)
	issued := harness.IssueCredential(principal)

	modernID := json.RawMessage(`"modern-self"`)
	modern := harness.ModernSelfServiceCall(issued.Bearer, modernID, "get_identity", struct{}{})
	identity := decodeSelfServiceResult[contract.GetIdentityResult](t, modern, modernID, contract.SummaryIdentityReturned)
	assert.Equal(t, principal.Resource.ID, identity.Identity.ID)

	session, _ := harness.LegacyInitialize(issued.Bearer, json.RawMessage(`1`))
	legacyID := json.RawMessage(`"legacy-self"`)
	legacy := harness.LegacySelfServiceCall(issued.Bearer, session, legacyID, "get_identity", struct{}{})
	legacyIdentity := decodeSelfServiceResult[contract.GetIdentityResult](t, legacy, legacyID, contract.SummaryIdentityReturned)
	assert.Equal(t, principal.Resource.ID, legacyIdentity.Identity.ID)
	require.Equal(t, http.StatusNoContent, harness.LegacyDelete(issued.Bearer, session).StatusCode)

	checkpoint := harness.AuditCheckpoint()
	createID := json.RawMessage(`"create-request"`)
	createdResponse := harness.ModernSelfServiceCall(issued.Bearer, createID, "create_grant_request", contract.CreateGrantRequestInput{Policy: contract.Policy{Scope: contract.PolicyTool, Target: "s5-harness.echo"}})
	created := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, createdResponse, createID, contract.SummaryGrantRequestProcessed)
	require.Equal(t, contract.RequestCreated, created.Outcome)
	require.NotNil(t, created.Request)
	require.Len(t, harness.WaitAuditAfter(checkpoint), 1)

	listed := harness.ListGrantRequests(principal.Resource.ID)
	require.Len(t, listed, 1)
	request := harness.GetGrantRequest(created.Request.ID)
	descriptor := harness.GetOnlyDescriptor(catalog.ServerID)
	assertDescriptorEvidenceMatches(t, request.Resource.SubmittedEvidence, descriptor, catalog.Namespace)

	beforeRestart := request.Resource
	harness.Restart()
	afterRestart := harness.GetGrantRequest(beforeRestart.ID)
	assert.Equal(t, beforeRestart.GrantRequestSummary, afterRestart.Resource.GrantRequestSummary)

	lossCheckpoint := harness.AuditCheckpoint()
	lost := harness.ModernCallDiscardResponse(issued.Bearer, json.RawMessage(`"lost-read"`), "mcp_gateway.get_identity", json.RawMessage(`{}`))
	require.Len(t, harness.WaitAuditAfter(lossCheckpoint), 1)
	require.NoError(t, <-lost)

	result := harness.Stop(syscall.SIGTERM)
	connection, err = net.DialTimeout("tcp", harness.authority, 50*time.Millisecond)
	if connection != nil {
		_ = connection.Close()
	}
	require.Error(t, err, "stopped Gateway listener remained reachable")
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
	assert.Zero(t, catalog.CallCount())
	harness.AssertCanaryAbsent([]byte("s5-harness-artifact-canary"))
}
