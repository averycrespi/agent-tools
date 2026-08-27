//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryEvictsOldestPreseededInvocationAndKeepsPrivateCallDataOutOfArtifacts(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	catalog := harness.SetupCurrentCatalog("retention", []fixtureTool{{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("Retention caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("alpha")})
	harness.Stop(syscall.SIGTERM)
	seedInvocationHistory(t, harness.root, 4096)

	harness.Start()
	waitForStdioServer(t, harness, catalog.ServerID, activeCatalog)
	catalog.Fixture.SetCallOutcome(fixtureCallPrivateSuccess)
	const argumentCanary = "e2e-retention-private-argument-canary"
	response := harness.ModernCall(issued.Bearer, json.RawMessage(`"retention"`), "retention.alpha", json.RawMessage(`{"token":"`+argumentCanary+`"}`))
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.True(t, bytes.Contains(response.Body, []byte(fixturePrivateSuccessText)), "private success was not returned to its caller")
	clear(response.Body)

	events := harness.OpenEvents()
	require.Equal(t, http.StatusOK, events.StatusCode)
	eventReader := newBoundedEventReader(events.Body)
	evidence := [][]byte{eventReader.frame(t)}
	backupResponse := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/backups", []byte(`{}`), map[string]string{"Idempotency-Key": "invocation-retention"})
	var artifact contract.Backup
	decodeSnapshot(t, backupResponse, http.StatusCreated, &artifact)
	evidence = append(evidence, append([]byte(nil), backupResponse.Body...), eventReader.frame(t), eventReader.frame(t))
	require.NoError(t, events.Body.Close())
	fixtureEvidence, err := json.Marshal(catalog.Fixture.Events())
	require.NoError(t, err)
	evidence = append(evidence, fixtureEvidence)
	result := harness.Stop(syscall.SIGTERM)

	observations := harness.AuditObservations()
	require.Len(t, observations, 4096)
	assert.Equal(t, int64(2), observations[0].Sequence)
	assert.Equal(t, seededInvocationID(1), observations[0].InvocationID)
	assert.NotEqual(t, seededInvocationID(0), observations[0].InvocationID)
	last := observations[len(observations)-1]
	assert.Equal(t, int64(4097), last.Sequence)
	assert.Equal(t, contract.AdmissionEvaluated, last.AdmissionClass)
	assert.Equal(t, contract.DecisionAllow, last.Decision)
	assert.Equal(t, contract.TerminalSucceeded, last.TerminalClass)
	assert.Equal(t, 1, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"))
	assertBackupArtifactModes(t, harness.root, artifact.ID)
	scanInvocationPrivacySinks(t, harness, issued.Bearer, []string{argumentCanary, fixturePrivateSuccessText}, evidence, result)
}

func TestGatewayBinaryPersistsNoRawToolErrorOrSensitiveArgument(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	catalog := harness.SetupCurrentCatalog("privacy", []fixtureTool{{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("Privacy caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("alpha")})
	catalog.Fixture.SetCallOutcome(fixtureCallToolError)
	const argumentCanary = "e2e-tool-error-private-argument-canary"
	response := harness.ModernCall(issued.Bearer, json.RawMessage(`"tool-error"`), "privacy.alpha", json.RawMessage(`{"access_token":"`+argumentCanary+`"}`))
	assertCallError(t, response, json.RawMessage(`"tool-error"`), contract.DownstreamFailure, false)
	evidence := [][]byte{append([]byte(nil), response.Body...)}
	clear(response.Body)
	backupResponse := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/backups", []byte(`{}`), map[string]string{"Idempotency-Key": "invocation-privacy"})
	var artifact contract.Backup
	decodeSnapshot(t, backupResponse, http.StatusCreated, &artifact)
	evidence = append(evidence, append([]byte(nil), backupResponse.Body...))
	fixtureEvidence, err := json.Marshal(catalog.Fixture.Events())
	require.NoError(t, err)
	evidence = append(evidence, fixtureEvidence)
	result := harness.Stop(syscall.SIGTERM)

	observations := harness.AuditObservations()
	require.Len(t, observations, 1)
	assert.Equal(t, contract.TerminalDownstreamFailure, observations[0].TerminalClass)
	assertBackupArtifactModes(t, harness.root, artifact.ID)
	scanInvocationPrivacySinks(t, harness, issued.Bearer, []string{argumentCanary, fixtureToolErrorText}, evidence, result)
}

func seedInvocationHistory(t *testing.T, root string, count int) {
	t.Helper()
	require.Equal(t, 4096, count)
	ctx := context.Background()
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Open(ctx, ownership)
	require.NoError(t, err)
	err = store.Mutate(ctx, func(transaction *sql.Tx) error {
		statement, prepareErr := transaction.PrepareContext(ctx, `INSERT INTO invocations (
			id, principal_id, credential_id, credential_fingerprint, credential_revision,
			admitted_at, admission_class
		) VALUES (?, ?, ?, ?, ?, ?, ?)`)
		if prepareErr != nil {
			return prepareErr
		}
		defer func() { _ = statement.Close() }()
		for index := range count {
			if _, insertErr := statement.ExecContext(ctx,
				seededInvocationID(index), "01M10F00000000000000000000", "01M10G00000000000000000000", "0123456789abcdef", 1,
				"2026-08-26T00:00:00.000000000Z", string(contract.AdmissionInvalidParams),
			); insertErr != nil {
				return insertErr
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, store.Checkpoint(ctx))
	require.NoError(t, store.Close())
	require.NoError(t, ownership.MarkClean())
	require.NoError(t, ownership.Close())
}

func seededInvocationID(index int) string {
	return "01M10E" + fmt.Sprintf("%020d", index)
}

func scanInvocationPrivacySinks(t *testing.T, harness *gatewayHarness, agent *agentBearer, values []string, evidence [][]byte, result testutil.ProcessResult) {
	t.Helper()
	type scanner struct {
		name string
		scan func(string, io.Reader) error
	}
	scanners := []scanner{{name: "agent bearer", scan: agent.scan}}
	adminScanner, err := testutil.NewCanaryScanner([]byte(harness.bearer))
	require.NoError(t, err)
	scanners = append(scanners, scanner{name: "admin bearer", scan: adminScanner.Scan})
	for index, value := range values {
		valueScanner, scannerErr := testutil.NewCanaryScanner([]byte(value))
		require.NoError(t, scannerErr)
		scanners = append(scanners, scanner{name: "private value " + strconv.Itoa(index), scan: valueScanner.Scan})
	}
	results := []testutil.ProcessResult{harness.initialization, result}
	for _, current := range results {
		require.False(t, current.StdoutTruncated)
		require.False(t, current.StderrTruncated)
	}
	for _, currentScanner := range scanners {
		for index, current := range results {
			require.NoError(t, currentScanner.scan(currentScanner.name+" stdout "+strconv.Itoa(index), bytes.NewReader(current.Stdout)))
			require.NoError(t, currentScanner.scan(currentScanner.name+" stderr "+strconv.Itoa(index), bytes.NewReader(current.Stderr)))
		}
		for index, artifact := range evidence {
			require.NoError(t, currentScanner.scan(currentScanner.name+" evidence "+strconv.Itoa(index), bytes.NewReader(artifact)))
		}
		require.NoError(t, filepath.Walk(harness.root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || !info.Mode().IsRegular() {
				return walkErr
			}
			file, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			return errors.Join(currentScanner.scan(currentScanner.name+" data artifact", file), file.Close())
		}))
	}
}
