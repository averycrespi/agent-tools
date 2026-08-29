//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2EGrantRequestRecoveryPrivacy(t *testing.T) {
	harness := newGatewayHarness(t)
	initialAdmin := harness.bearer
	harness.Start()
	defer func() {
		if harness.process != nil {
			harness.Stop(syscall.SIGTERM)
		}
	}()
	catalog := harness.SetupCurrentCatalog("s5-recovery", []fixtureTool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("S5 recovery agent", contract.VisibilityRequestable)
	credentialA := harness.IssueCredential(principal)

	events := harness.OpenEvents()
	require.Equal(t, http.StatusOK, events.StatusCode)
	eventReader := newBoundedEventReader(events.Body)
	assert.Equal(t, ": keepalive\n\n", string(eventReader.frame(t)))

	policy := contract.Policy{Scope: contract.PolicyTool, Target: "s5-recovery.echo"}
	checkpoint := harness.AuditCheckpoint()
	lost := harness.ModernCallDiscardResponse(credentialA.Bearer, json.RawMessage(`"lost-create"`), "mcp_gateway.create_grant_request", marshalHarnessJSON(t, contract.CreateGrantRequestInput{Policy: policy}))
	require.Len(t, harness.WaitAuditAfter(checkpoint), 1)
	require.NoError(t, <-lost)
	requestEvent := eventReader.frame(t)
	assert.Equal(t, "event: invalidate\ndata: {\"kind\":\"grant_requests\",\"resource_id\":null}\n\n", string(requestEvent))

	retryID := json.RawMessage(`"recover-create"`)
	retryResponse := harness.ModernSelfServiceCall(credentialA.Bearer, retryID, "create_grant_request", contract.CreateGrantRequestInput{Policy: policy})
	recovered := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, retryResponse, retryID, contract.SummaryGrantRequestProcessed)
	require.Equal(t, contract.RequestExisting, recovered.Outcome)
	require.NotNil(t, recovered.Request)
	assert.Zero(t, catalog.CallCount(), "lost local response must not replay downstream")

	statusResponse := harness.adminSnapshot(http.MethodGet, "/api/v1/system-status", nil)
	var status contract.SystemStatus
	decodeSnapshot(t, statusResponse, http.StatusOK, &status)
	assert.Equal(t, int64(1), status.Limits.GrantRequests.InUse)
	assert.Positive(t, status.Limits.GrantRequestEvidenceBytes.InUse)
	beforeDrift := harness.GetGrantRequest(recovered.Request.ID)
	require.NotNil(t, beforeDrift.Resource.SubmittedEvidence)

	catalog.Fixture.SetTools([]fixtureTool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`)}})
	refresh := createServerOperation(t, harness, catalog.ServerID, catalog.ETag, string(contract.OperationRefreshCatalog), "s5-recovery-drift")
	harness.WaitOperation(catalog.ServerID, refresh.ID, contract.OperationSucceeded)
	afterDrift := harness.GetGrantRequest(recovered.Request.ID)
	require.NotNil(t, afterDrift.Resource.SubmittedEvidence)
	require.NotNil(t, afterDrift.Resource.CurrentTarget.Fingerprint)
	assert.Equal(t, beforeDrift.Resource.SubmittedEvidence.Fingerprint, afterDrift.Resource.SubmittedEvidence.Fingerprint)
	assert.NotEqual(t, afterDrift.Resource.SubmittedEvidence.Fingerprint, *afterDrift.Resource.CurrentTarget.Fingerprint)

	backupResponse := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/backups", []byte(`{}`), map[string]string{"Idempotency-Key": "s5-recovery"})
	var artifact contract.Backup
	decodeSnapshot(t, backupResponse, http.StatusCreated, &artifact)
	assert.Equal(t, "10", artifact.SchemaVersion)
	assertBackupArtifactModes(t, harness.root, artifact.ID)
	require.NoError(t, events.Body.Close())

	credentialB := harness.IssueCredential(credentialA.Principal)
	assertAuthenticationProblem(t, harness.ModernList(credentialA.Bearer, json.RawMessage(`"rotated-a"`), ""))
	harness.Restart()
	waitForS5RecoveryCatalog(t, harness, catalog.ServerID)
	persistedID := json.RawMessage(`"persisted-after-restart"`)
	persistedResponse := waitForSelfServiceResponse(t, func() responseSnapshot {
		return harness.ModernSelfServiceCall(credentialB.Bearer, persistedID, "get_grant_request", contract.GrantRequestIDInput{ID: recovered.Request.ID})
	})
	persisted := decodeSelfServiceResult[contract.GetGrantRequestResult](t, persistedResponse, persistedID, contract.SummaryGrantRequestReturned)
	assert.Equal(t, contract.RequestFound, persisted.Outcome)

	harness.Stop(syscall.SIGTERM)
	restoreResult := harness.RestoreBackup(artifact.ID)
	var restoredCommand struct {
		OK             bool   `json:"ok"`
		Operation      string `json:"operation"`
		Mode           string `json:"mode"`
		InstallationID string `json:"installation_id"`
		Revision       string `json:"revision"`
		BackupID       string `json:"backup_id"`
	}
	require.NoError(t, json.Unmarshal(restoreResult.Stdout, &restoredCommand))
	assert.True(t, restoredCommand.OK)
	assert.Equal(t, "restore", restoredCommand.Operation)
	assert.Equal(t, "backup", restoredCommand.Mode)
	assert.Equal(t, artifact.InstallationID, restoredCommand.InstallationID)
	assert.Equal(t, artifact.ID, restoredCommand.BackupID)
	sourceRevision, err := strconv.Atoi(artifact.SourceRevision)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(sourceRevision+1), restoredCommand.Revision)
	restoredAdmin := harness.bearer
	harness.Start()
	waitForS5RecoveryCatalog(t, harness, catalog.ServerID)
	assertAuthenticationProblem(t, harness.ModernList(credentialB.Bearer, json.RawMessage(`"restored-b"`), ""))
	restoredPrincipal := harness.GetPrincipal(principal.Resource.ID)
	credentialC := harness.IssueCredential(restoredPrincipal)
	restoredID := json.RawMessage(`"restored-request"`)
	restoredResponse := waitForSelfServiceResponse(t, func() responseSnapshot {
		return harness.ModernSelfServiceCall(credentialC.Bearer, restoredID, "get_grant_request", contract.GrantRequestIDInput{ID: recovered.Request.ID})
	})
	restored := decodeSelfServiceResult[contract.GetGrantRequestResult](t, restoredResponse, restoredID, contract.SummaryGrantRequestReturned)
	assert.Equal(t, contract.RequestFound, restored.Outcome)
	restoredStatusResponse := harness.adminSnapshot(http.MethodGet, "/api/v1/system-status", nil)
	decodeSnapshot(t, restoredStatusResponse, http.StatusOK, &status)
	assert.Equal(t, int64(1), status.Limits.GrantRequests.InUse)
	assert.Positive(t, status.Limits.GrantRequestEvidenceBytes.InUse)
	assert.Zero(t, catalog.CallCount())

	result := harness.Stop(syscall.SIGTERM)
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
	assertAgentBearerAbsent(t, harness, credentialA.Bearer)
	assertAgentBearerAbsent(t, harness, credentialB.Bearer)
	assertAgentBearerAbsent(t, harness, credentialC.Bearer)
	harness.AssertCanaryAbsent([]byte(initialAdmin))
	harness.AssertCanaryAbsent([]byte(restoredAdmin))
	for _, process := range append([]testutil.ProcessResult{harness.initialization}, harness.results...) {
		assert.False(t, process.StdoutTruncated)
		assert.False(t, process.StderrTruncated)
	}

	t.Run("malformed request state rejects startup", func(t *testing.T) {
		malformed := newGatewayHarness(t)
		ownership, err := gatewaypaths.Acquire(malformed.root)
		require.NoError(t, err)
		store, err := storage.Open(context.Background(), ownership)
		require.NoError(t, err)
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			_, mutationErr := transaction.Exec(`UPDATE grant_request_evidence_bytes SET total_bytes = 1 WHERE singleton = 1`)
			return mutationErr
		}))
		require.NoError(t, store.Close())
		require.NoError(t, ownership.MarkClean())
		require.NoError(t, ownership.Close())
		runner, err := testutil.NewBinaryRunner(5*time.Second, 16*1024)
		require.NoError(t, err)
		failed, runErr := runner.Run(t.Context(), malformed.binary, malformed.serveArgs...)
		require.Error(t, runErr)
		assert.NotErrorIs(t, runErr, context.DeadlineExceeded)
		assert.Empty(t, failed.Stdout)
		assert.Contains(t, string(failed.Stderr), "could not be started safely")
		assert.True(t, failed.Cleanup.Reaped)
		assert.False(t, failed.Cleanup.Survived)
	})
}

func waitForS5RecoveryCatalog(t *testing.T, harness *gatewayHarness, serverID string) {
	t.Helper()
	waitForStdioServer(t, harness, serverID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveState == contract.ActiveCatalogCurrent
	})
}

func waitForSelfServiceResponse(t *testing.T, call func() responseSnapshot) responseSnapshot {
	t.Helper()
	var response responseSnapshot
	require.Eventually(t, func() bool {
		response = call()
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		return json.Unmarshal(response.Body, &envelope) == nil && len(envelope.Result) > 0
	}, 3*time.Second, 10*time.Millisecond)
	return response
}

func assertAgentBearerAbsent(t *testing.T, harness *gatewayHarness, bearer *agentBearer) {
	t.Helper()
	for index, result := range append([]testutil.ProcessResult{harness.initialization}, harness.results...) {
		bearer.assertAbsent(t, fmt.Sprintf("process-%d-stdout", index), bytes.NewReader(result.Stdout))
		bearer.assertAbsent(t, fmt.Sprintf("process-%d-stderr", index), bytes.NewReader(result.Stderr))
	}
	require.NoError(t, filepath.Walk(harness.root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		scanErr := bearer.scan(strings.TrimPrefix(path, harness.root), file)
		closeErr := file.Close()
		if scanErr != nil {
			return scanErr
		}
		return closeErr
	}))
}
