//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type destructiveServerView struct {
	ID                  string                         `json:"id"`
	Namespace           string                         `json:"namespace"`
	DesiredState        contract.DesiredServerState    `json:"desired_state"`
	DesiredRevision     string                         `json:"desired_revision"`
	CredentialRevisions contract.CredentialRevisions   `json:"credential_revisions"`
	CredentialState     contract.ServerCredentialState `json:"credential_state"`
	Runtime             contract.ServerRuntime         `json:"runtime"`
	Catalog             contract.ServerCatalog         `json:"catalog"`
	DeletedAt           *string                        `json:"deleted_at"`
}

func TestGatewayBinaryDisconnectDisableDeleteAndIsolation(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)

	unrelatedFixture := newRawHTTPFixture(t, "modern")
	unrelated, _ := createHTTPServer(t, harness, unrelatedFixture.URL(), "modern")
	harness.WaitOperation(unrelated.Server.ID, unrelated.Operation.ID, contract.OperationSucceeded)
	unrelatedBefore := getDestructiveServer(t, harness, unrelated.Server.ID)
	unrelatedRequests := len(unrelatedFixture.Events())

	executable, err := os.Executable()
	require.NoError(t, err)
	authDirectory := t.TempDir()
	authEvents := filepath.Join(authDirectory, "events.jsonl")
	authID, authETag := createAndActivateStaticServer(t, harness, executable, authDirectory, authEvents)
	authBefore := getDestructiveServer(t, harness, authID)
	assert.Equal(t, "1", authBefore.CredentialRevisions.StaticCredential)
	assert.Equal(t, contract.ServerCredentialReady, authBefore.CredentialState)
	authStarts := fixtureEvents(waitForFixtureEvents(t, authEvents, func(events []stdioFixtureEvent) bool { return countFixtureEvents(events, "start", "") == 1 }), "start", "")
	disconnect := createServerOperation(t, harness, authID, authETag, string(contract.OperationDisconnectCredentials), "disconnect-current")
	harness.WaitOperation(authID, disconnect.ID, contract.OperationSucceeded)
	authAfter := waitForDestructiveServer(t, harness, authID, func(server destructiveServerView) bool {
		return server.CredentialRevisions.StaticCredential == "2" && server.CredentialState == contract.ServerCredentialAbsent && server.Runtime.RuntimeID == nil && server.Runtime.Reconciliation.InUse == 0 && server.Catalog.ActiveRevision == nil
	})
	assert.Equal(t, contract.RuntimeInactive, authAfter.Runtime.State)
	assert.Equal(t, contract.DurableCatalogUnavailable, authAfter.Catalog.DurableState)
	assert.Equal(t, contract.ActiveCatalogUnavailable, authAfter.Catalog.ActiveState)
	assert.Equal(t, int64(0), authAfter.Catalog.ActiveToolCount)
	assert.False(t, processExists(authStarts[0].PID), "disconnect operation succeeded before process reap")
	assert.Equal(t, 1, countFixtureEvents(fixtureEventsNow(t, authEvents), "start", ""), "disconnect must not start a replacement")
	reload := createServerOperation(t, harness, authID, authETag, string(contract.OperationReload), "disconnected-reload")
	failedReload := harness.WaitOperation(authID, reload.ID, contract.OperationFailed)
	require.NotNil(t, failedReload.Reason)
	assert.Equal(t, contract.ReasonCredentialAbsent, *failedReload.Reason)
	waitForDestructiveServer(t, harness, authID, func(server destructiveServerView) bool { return server.Runtime.Reconciliation.InUse == 0 })
	assert.Equal(t, 1, countFixtureEvents(fixtureEventsNow(t, authEvents), "start", ""), "reload must not resurrect disconnected authority")
	assertUnrelatedServerContinuity(t, harness, unrelatedFixture, unrelatedBefore, unrelatedRequests)

	disableDirectory := t.TempDir()
	disableEvents := filepath.Join(disableDirectory, "events.jsonl")
	disable := createStdioServer(t, harness, executable, "disable-modern", filepath.Join(disableDirectory, "marker"), disableEvents)
	harness.WaitOperation(disable.Server.ID, disable.Operation.ID, contract.OperationSucceeded)
	disableStarts := fixtureEvents(waitForFixtureEvents(t, disableEvents, func(events []stdioFixtureEvent) bool { return countFixtureEvents(events, "start", "") == 1 }), "start", "")
	disableMutation, _ := patchServer(t, harness, disable.Server.ID, contract.ServerETag(disable.Server.ID, "1"), `{"enabled":false}`)
	require.NotNil(t, disableMutation.Operation)
	assert.Equal(t, contract.OperationDisable, disableMutation.Operation.Kind)
	harness.WaitOperation(disable.Server.ID, disableMutation.Operation.ID, contract.OperationSucceeded)
	disabled := waitForDestructiveServer(t, harness, disable.Server.ID, func(server destructiveServerView) bool {
		return server.DesiredState == contract.DesiredServerDisabled && server.Runtime.State == contract.RuntimeInactive && server.Runtime.RuntimeID == nil && server.Runtime.Reconciliation.InUse == 0 && server.Catalog.ActiveRevision == nil
	})
	assert.Equal(t, contract.DurableCatalogStale, disabled.Catalog.DurableState)
	assert.Equal(t, contract.ActiveCatalogAbsent, disabled.Catalog.ActiveState)
	assert.Equal(t, int64(0), disabled.Catalog.ActiveToolCount)
	assert.False(t, processExists(disableStarts[0].PID), "disable operation succeeded before process reap")
	assert.Equal(t, 1, countFixtureEvents(fixtureEventsNow(t, disableEvents), "start", ""))
	assertUnrelatedServerContinuity(t, harness, unrelatedFixture, unrelatedBefore, unrelatedRequests)

	deleteDirectory := t.TempDir()
	deleteEvents := filepath.Join(deleteDirectory, "events.jsonl")
	deleted := createStdioServer(t, harness, executable, "delete-modern", filepath.Join(deleteDirectory, "marker"), deleteEvents)
	harness.WaitOperation(deleted.Server.ID, deleted.Operation.ID, contract.OperationSucceeded)
	deleteStarts := fixtureEvents(waitForFixtureEvents(t, deleteEvents, func(events []stdioFixtureEvent) bool { return countFixtureEvents(events, "start", "") == 1 }), "start", "")
	var tombstone replacementMutation
	response := harness.AdminJSON(http.MethodDelete, "/api/v1/servers/"+deleted.Server.ID, `{}`, map[string]string{"If-Match": contract.ServerETag(deleted.Server.ID, "1")}, &tombstone)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	tombstoneETag := response.Header.Get("ETag")
	require.NoError(t, response.Body.Close())
	require.NotNil(t, tombstone.Operation)
	assert.Equal(t, contract.OperationDelete, tombstone.Operation.Kind)
	harness.WaitOperation(deleted.Server.ID, tombstone.Operation.ID, contract.OperationSucceeded)
	deletedView := waitForDestructiveServer(t, harness, deleted.Server.ID, func(server destructiveServerView) bool {
		return server.DesiredState == contract.DesiredServerDeleted && server.Runtime.State == contract.RuntimeDeleted && server.Runtime.RuntimeID == nil && server.Catalog.ActiveRevision == nil && server.DeletedAt != nil
	})
	assert.Equal(t, contract.DurableCatalogRetired, deletedView.Catalog.DurableState)
	assert.Equal(t, contract.ActiveCatalogAbsent, deletedView.Catalog.ActiveState)
	assert.False(t, processExists(deleteStarts[0].PID), "delete operation succeeded before process reap")
	assert.Equal(t, 1, countFixtureEvents(fixtureEventsNow(t, deleteEvents), "start", ""))

	var replay replacementMutation
	response = harness.AdminJSON(http.MethodDelete, "/api/v1/servers/"+deleted.Server.ID, `{}`, map[string]string{"If-Match": tombstoneETag}, &replay)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, tombstoneETag, response.Header.Get("ETag"))
	require.NoError(t, response.Body.Close())
	require.NotNil(t, replay.Operation)
	assert.Equal(t, tombstone.Operation.ID, replay.Operation.ID)
	assert.Equal(t, tombstone.Server.DesiredRevision, replay.Server.DesiredRevision)
	response = harness.AdminJSON(http.MethodDelete, "/api/v1/servers/"+deleted.Server.ID, `{}`, map[string]string{"If-Match": contract.ServerETag(deleted.Server.ID, "1")}, nil)
	assert.Equal(t, http.StatusPreconditionFailed, response.StatusCode)
	_ = readResponseBody(t, response)
	assertServerListContainsOnce(t, harness, deleted.Server.ID)
	assertUnrelatedServerContinuity(t, harness, unrelatedFixture, unrelatedBefore, unrelatedRequests)

	var catalog contract.CatalogPage
	response = harness.AdminJSON(http.MethodGet, "/api/v1/catalog", "", nil, &catalog)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.Len(t, catalog.Items, 2)
	for _, item := range catalog.Items {
		assert.Equal(t, unrelated.Server.ID, item.ServerID)
	}
	assert.Equal(t, contract.AggregateCatalogDegraded, catalog.Catalog.ActiveState)
}

func createAndActivateStaticServer(t *testing.T, harness *gatewayHarness, executable, directory, eventsPath string) (string, string) {
	t.Helper()
	request := map[string]any{
		"namespace": "disconnect-target", "display_name": "Disconnect target", "enabled": false,
		"transport": map[string]any{
			"kind": "stdio", "executable": executable,
			"arguments":         []string{"-test.run=^TestE2EStdioFixtureProcess$", "--", "mcp", "modern", filepath.Join(directory, "marker"), eventsPath},
			"working_directory": directory, "environment": map[string]string{stdioFixtureEnvironment: "1"}, "secret_environment": map[string]string{"TOKEN": "token"},
		},
	}
	contents, err := json.Marshal(request)
	require.NoError(t, err)
	var responseBody json.RawMessage
	response := harness.AdminJSON(http.MethodPost, "/api/v1/servers", string(contents), map[string]string{"Idempotency-Key": "disconnect-target"}, &responseBody)
	require.Equal(t, http.StatusCreated, response.StatusCode, string(responseBody))
	etag := response.Header.Get("ETag")
	require.NoError(t, response.Body.Close())
	var created replacementMutation
	require.NoError(t, json.Unmarshal(responseBody, &created))
	assert.Nil(t, created.Operation)

	var replacement contract.CredentialReplacementResult
	response = harness.AdminJSON(http.MethodPost, "/api/v1/servers/"+created.Server.ID+"/credential-replacements", `{"kind":"static_credential","expected_revision":"0","values":{"token":"disconnect-canary"}}`, map[string]string{"If-Match": etag}, &replacement)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "1", replacement.CredentialRevision)
	harness.WaitOperation(created.Server.ID, replacement.Operation.ID, contract.OperationSucceeded)

	enabled, etag := patchServer(t, harness, created.Server.ID, etag, `{"enabled":true}`)
	require.NotNil(t, enabled.Operation)
	harness.WaitOperation(created.Server.ID, enabled.Operation.ID, contract.OperationSucceeded)
	waitForDestructiveServer(t, harness, created.Server.ID, func(server destructiveServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Runtime.Reconciliation.InUse == 0 && server.Catalog.ActiveToolCount == 2 && server.CredentialState == contract.ServerCredentialReady
	})
	return created.Server.ID, etag
}

func getDestructiveServer(t *testing.T, harness *gatewayHarness, serverID string) destructiveServerView {
	t.Helper()
	var server destructiveServerView
	response := harness.AdminJSON(http.MethodGet, "/api/v1/servers/"+serverID, "", nil, &server)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	return server
}

func waitForDestructiveServer(t *testing.T, harness *gatewayHarness, serverID string, ready func(destructiveServerView) bool) destructiveServerView {
	t.Helper()
	var result destructiveServerView
	waitForStdioServer(t, harness, serverID, func(stdioServerView) bool {
		result = getDestructiveServer(t, harness, serverID)
		return ready(result)
	})
	return result
}

func assertUnrelatedServerContinuity(t *testing.T, harness *gatewayHarness, fixture *rawHTTPFixture, before destructiveServerView, requestCount int) {
	t.Helper()
	after := getDestructiveServer(t, harness, before.ID)
	require.NotNil(t, before.Runtime.RuntimeID)
	require.NotNil(t, after.Runtime.RuntimeID)
	require.NotNil(t, before.Catalog.ActiveRevision)
	require.NotNil(t, after.Catalog.ActiveRevision)
	assert.Equal(t, *before.Runtime.RuntimeID, *after.Runtime.RuntimeID)
	assert.Equal(t, *before.Catalog.ActiveRevision, *after.Catalog.ActiveRevision)
	assert.Equal(t, requestCount, len(fixture.Events()))
}

func assertServerListContainsOnce(t *testing.T, harness *gatewayHarness, serverID string) {
	t.Helper()
	var page struct {
		Items []destructiveServerView `json:"items"`
	}
	response := harness.AdminJSON(http.MethodGet, "/api/v1/servers?limit=100", "", nil, &page)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	count := 0
	for _, item := range page.Items {
		if item.ID == serverID {
			count++
			assert.Equal(t, contract.DesiredServerDeleted, item.DesiredState)
		}
	}
	assert.Equal(t, 1, count)
}
