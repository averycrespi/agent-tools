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
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/averycrespi/agent-tools/mcp-gateway/test/acceptance"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6E2EInvocationReadPrivacy(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	strictSchema := json.RawMessage(`{"type":"object","properties":{"note":{"type":"string"},"token":{"type":"string"}},"additionalProperties":false}`)
	catalog := harness.SetupCurrentCatalog("invocation-read", []fixtureTool{
		{Name: "allowed", InputSchema: strictSchema},
		{Name: "blocked", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	principal := harness.CreatePrincipal("Invocation reader", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("allowed")})

	unknown := harness.ModernCall(issued.Bearer, json.RawMessage(`"unknown"`), "invocation-read.absent", json.RawMessage(`{}`))
	assertCallRejected(t, unknown, json.RawMessage(`"unknown"`))
	blocked := harness.ModernCall(issued.Bearer, json.RawMessage(`"blocked"`), "invocation-read.blocked", json.RawMessage(`{}`))
	assertCallRejected(t, blocked, json.RawMessage(`"blocked"`))

	const argumentCanary = "s6-invocation-secret-argument-canary"
	const inertCapture = "<script>window.invocationCanary=true</script>"
	catalog.Fixture.SetCallOutcome(fixtureCallPrivateSuccess)
	privateSuccess := harness.ModernCall(issued.Bearer, json.RawMessage(`"private"`), "invocation-read.allowed", json.RawMessage(`{"note":"`+inertCapture+`","token":"`+argumentCanary+`"}`))
	require.Equal(t, http.StatusOK, privateSuccess.StatusCode, string(privateSuccess.Body))
	assert.Contains(t, string(privateSuccess.Body), fixturePrivateSuccessText)
	clear(privateSuccess.Body)

	catalog.Fixture.SetCallOutcome(fixtureCallToolError)
	toolError := harness.ModernCall(issued.Bearer, json.RawMessage(`"tool-error"`), "invocation-read.allowed", json.RawMessage(`{"note":"safe"}`))
	assertCallError(t, toolError, json.RawMessage(`"tool-error"`), contract.DownstreamFailure, false)
	catalog.Fixture.SetCallOutcome(fixtureCallSuccess)

	barrier := catalog.Fixture.Arm("tools/call")
	callDone := make(chan responseSnapshot, 1)
	go func() {
		callDone <- harness.ModernCall(issued.Bearer, json.RawMessage(`"missing-terminal"`), "invocation-read.allowed", json.RawMessage(`{"note":"held"}`))
	}()
	awaitFixtureSignal(t, barrier.entered, "committed invocation did not reach the fixture barrier")
	callsBeforeMissingRead := catalog.CallCount()
	missingResponse, missingPage := listInvocations(t, harness, url.Values{"limit": {"100"}})
	assert.Equal(t, callsBeforeMissingRead, catalog.CallCount(), "invocation reads must not replay downstream work")
	require.Len(t, missingPage.Items, 5)
	bases := make(map[contract.InvocationOutcomeBasis]bool)
	var missingID, admissionID, privateID string
	for _, item := range missingPage.Items {
		bases[item.Outcome.Basis] = true
		if item.Outcome.Basis == contract.InvocationBasisMissingTerminal {
			missingID = item.ID
			assert.Equal(t, contract.InvocationOutcomeUnknown, item.Outcome.Class)
			assert.Nil(t, item.Outcome.CompletedAt)
		}
		if item.Outcome.Basis == contract.InvocationBasisAdmission {
			admissionID = item.ID
		}
		if item.Outcome.Basis == contract.InvocationBasisTerminal && item.Outcome.Class == contract.InvocationOutcomeSucceeded && item.RequestedName != nil && *item.RequestedName == "invocation-read.allowed" {
			privateID = item.ID
		}
	}
	assert.Equal(t, map[contract.InvocationOutcomeBasis]bool{
		contract.InvocationBasisAdmission: true, contract.InvocationBasisPolicy: true,
		contract.InvocationBasisTerminal: true, contract.InvocationBasisMissingTerminal: true,
	}, bases)
	require.NotEmpty(t, missingID)
	require.NotEmpty(t, admissionID)
	require.NotEmpty(t, privateID)
	assert.NotContains(t, string(missingResponse.Body), `"pending"`)
	barrier.Release()
	completed := <-callDone
	require.Equal(t, http.StatusOK, completed.StatusCode, string(completed.Body))

	local := harness.ModernSelfServiceCall(issued.Bearer, json.RawMessage(`"local"`), "get_identity", map[string]any{})
	require.Equal(t, http.StatusOK, local.StatusCode, string(local.Body))
	callsBeforeReads := catalog.CallCount()
	rowsBeforeReads := harness.LiveAuditObservations()
	require.Len(t, rowsBeforeReads, 6)

	events := harness.OpenEvents()
	require.Equal(t, http.StatusOK, events.StatusCode)
	eventReader := newBoundedEventReader(events.Body)
	keepalive := eventReader.frame(t)
	assert.Equal(t, ": keepalive\n\n", string(keepalive))

	allResponse, allPage := listInvocations(t, harness, url.Values{"limit": {"100"}})
	require.Len(t, allPage.Items, 6)
	assert.Equal(t, callsBeforeReads, catalog.CallCount())
	localSummary := allPage.Items[0]
	require.NotNil(t, localSummary.Target)
	assert.Equal(t, contract.InvocationTargetGateway, localSummary.Target.Kind)
	assert.Equal(t, contract.SyntheticServerID, localSummary.Target.ServerID)
	assert.Equal(t, contract.InvocationBasisTerminal, localSummary.Outcome.Basis)

	privateSummary := invocationByID(t, allPage.Items, privateID)
	itemResponse := harness.adminSnapshot(http.MethodGet, "/api/v1/invocations/"+privateSummary.ID, nil)
	var item contract.Invocation
	decodeSnapshot(t, itemResponse, http.StatusOK, &item)
	assert.JSONEq(t, `{"note":"`+inertCapture+`","token":"[REDACTED]"}`, string(item.RedactedArguments))
	assert.NotContains(t, string(itemResponse.Body), "<script>")
	assert.Contains(t, string(itemResponse.Body), `\u003cscript\u003e`)
	assert.NotContains(t, string(allResponse.Body), "redacted_arguments")
	assert.NotContains(t, string(allResponse.Body), inertCapture)
	assert.NotContains(t, string(itemResponse.Body), argumentCanary)
	assert.NotContains(t, string(itemResponse.Body), fixturePrivateSuccessText)
	assert.NotContains(t, string(itemResponse.Body), fixtureToolErrorText)

	unknownAfterCompletion, unknownPage := listInvocations(t, harness, url.Values{"limit": {"100"}, "outcome": {"outcome_unknown"}})
	assert.Empty(t, unknownPage.Items, string(unknownAfterCompletion.Body))
	newestResponse, newestPage := listInvocations(t, harness, url.Values{"limit": {"1"}})
	require.Len(t, newestPage.Items, 1)
	require.NotNil(t, newestPage.NextCursor)
	assert.Equal(t, localSummary.ID, newestPage.Items[0].ID)
	assert.Equal(t, rowsBeforeReads, harness.LiveAuditObservations(), "list and item reads must not mutate invocation evidence")

	// T7 owns the sole 4,096-row fixture; advance the retained floor directly to test the real API boundary without repeating it.
	simulateRetainedInvocationWindow(t, harness, localSummary.ID)
	staleResponse := harness.adminSnapshot(http.MethodGet, "/api/v1/invocations?limit=1&cursor="+url.QueryEscape(*newestPage.NextCursor), nil)
	assertProblem(t, staleResponse, http.StatusConflict, "stale_cursor", "The cursor snapshot is no longer available.", false)
	evictedResponse := harness.adminSnapshot(http.MethodGet, "/api/v1/invocations/"+admissionID, nil)
	assertProblem(t, evictedResponse, http.StatusNotFound, "not_found", "The resource was not found.", false)
	retainedResponse, retainedPage := listInvocations(t, harness, url.Values{"limit": {"100"}})
	require.Len(t, retainedPage.Items, 1)
	assert.Equal(t, localSummary.ID, retainedPage.Items[0].ID)
	assert.Equal(t, callsBeforeReads, catalog.CallCount(), "list/item/stale/evicted reads must not dispatch or replay")

	backupResponse := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/backups", []byte(`{}`), map[string]string{"Idempotency-Key": "s6-invocation-read-privacy"})
	var artifact contract.Backup
	decodeSnapshot(t, backupResponse, http.StatusCreated, &artifact)
	backupEvent := eventReader.frame(t)
	statusEvent := eventReader.frame(t)
	assert.Equal(t, "event: invalidate\ndata: {\"kind\":\"backups\",\"resource_id\":\""+artifact.ID+"\"}\n\n", string(backupEvent))
	assert.Equal(t, "event: invalidate\ndata: {\"kind\":\"system_status\",\"resource_id\":null}\n\n", string(statusEvent))
	require.NoError(t, events.Body.Close())

	fixtureEvidence, err := json.Marshal(catalog.Fixture.Events())
	require.NoError(t, err)
	evidence := [][]byte{
		missingResponse.Body, toolError.Body, completed.Body, local.Body, keepalive, allResponse.Body, itemResponse.Body,
		unknownAfterCompletion.Body, newestResponse.Body, staleResponse.Body, evictedResponse.Body, retainedResponse.Body,
		backupResponse.Body, backupEvent, statusEvent, fixtureEvidence,
	}
	result := harness.Stop(syscall.SIGTERM)
	assertBackupArtifactModes(t, harness.root, artifact.ID)
	scanInvocationPrivacySinks(t, harness, issued.Bearer, []string{argumentCanary, fixturePrivateSuccessText, fixtureToolErrorText}, evidence, result)
	assertInvocationReportSinks(t, []string{argumentCanary, fixturePrivateSuccessText, fixtureToolErrorText})
}

func listInvocations(t *testing.T, harness *gatewayHarness, query url.Values) (responseSnapshot, contract.InvocationPage) {
	t.Helper()
	path := "/api/v1/invocations"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	response := harness.adminSnapshot(http.MethodGet, path, nil)
	var page contract.InvocationPage
	decodeSnapshot(t, response, http.StatusOK, &page)
	return response, page
}

func invocationByID(t *testing.T, items []contract.InvocationSummary, id string) contract.InvocationSummary {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("invocation %s is absent", id)
	return contract.InvocationSummary{}
}

func simulateRetainedInvocationWindow(t *testing.T, harness *gatewayHarness, retainedID string) {
	t.Helper()
	databasePath := filepath.Join(harness.root, gatewaypaths.DatabaseName)
	databaseURL := (&url.URL{Scheme: "file", Path: databasePath, RawQuery: "_pragma=busy_timeout(2000)"}).String()
	database, err := sql.Open("sqlite3", databaseURL)
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	result, err := database.ExecContext(harness.ctx, `DELETE FROM invocations WHERE id <> ?`, retainedID)
	require.NoError(t, err)
	removed, err := result.RowsAffected()
	require.NoError(t, err)
	require.GreaterOrEqual(t, removed, int64(1))
	require.NoError(t, database.Close())
}

type invocationReportExecutor struct{ canary string }

func (executor invocationReportExecutor) Run(context.Context, string, acceptance.Command) ([]byte, error) {
	return []byte(executor.canary), errors.New(executor.canary)
}

func assertInvocationReportSinks(t *testing.T, canaries []string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	for _, canary := range canaries {
		report := acceptance.Run(context.Background(), root, invocationReportExecutor{canary: canary}, true)
		require.Equal(t, acceptance.ResultFailed, report.Result)
		encoded, err := json.Marshal(report)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), canary)
		for _, forbidden := range []string{`"stdout"`, `"stderr"`, `"error"`, `"output"`} {
			assert.NotContains(t, string(encoded), forbidden)
		}
	}
}

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
