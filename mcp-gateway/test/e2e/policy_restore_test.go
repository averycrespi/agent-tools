//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryRestoresPolicyWithoutRestoringAuthority(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	processResults := []testutil.ProcessResult{harness.initialization}
	var evidence [][]byte
	appendArgumentEvidence(t, &evidence, harness.initializationArgs, harness.serveArgs)

	names := discoveryNames("recovery", 98)
	visibleNames := withSyntheticNames(names)
	catalog := harness.SetupCurrentCatalog("recovery", fixtureTools(names))
	principal := harness.CreatePrincipal("Recovery agent", contract.VisibilityAll)
	credentialA := harness.IssueCredential(principal)
	grant := harness.CreateGrant(grantSpec{
		PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("conditional"),
	})

	statusResponse := harness.adminSnapshot(http.MethodGet, "/api/v1/system-status", nil)
	appendSnapshotEvidence(&evidence, statusResponse)
	var status contract.SystemStatus
	decodeSnapshot(t, statusResponse, http.StatusOK, &status)
	events := harness.OpenEvents()
	require.Equal(t, http.StatusOK, events.StatusCode)
	eventReader := newBoundedEventReader(events.Body)
	keepalive := eventReader.frame(t)
	assert.Equal(t, ": keepalive\n\n", string(keepalive))

	backupResponse := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/backups", []byte(`{}`), map[string]string{"Idempotency-Key": "t40-policy-restore"})
	appendSnapshotEvidence(&evidence, backupResponse)
	var artifact contract.Backup
	decodeSnapshot(t, backupResponse, http.StatusCreated, &artifact)
	assert.Equal(t, contract.MediaTypeJSON, backupResponse.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", backupResponse.Header.Get("Cache-Control"))
	assert.Equal(t, "10", artifact.SchemaVersion)
	assert.Equal(t, status.SQLite.Revision, artifact.SourceRevision)
	backupEvent := eventReader.frame(t)
	statusEvent := eventReader.frame(t)
	evidence = append(evidence, keepalive, backupEvent, statusEvent)
	assert.Equal(t, "event: invalidate\ndata: {\"kind\":\"backups\",\"resource_id\":\""+artifact.ID+"\"}\n\n", string(backupEvent))
	assert.Equal(t, "event: invalidate\ndata: {\"kind\":\"system_status\",\"resource_id\":null}\n\n", string(statusEvent))
	require.NoError(t, events.Body.Close())
	assertBackupArtifactModes(t, harness.root, artifact.ID)

	beforeBackupRestore := harness.ModernList(credentialA.Bearer, json.RawMessage(`"before-backup-restore"`), "")
	appendSnapshotEvidence(&evidence, beforeBackupRestore)
	backupCursor := discoveryCursor(t, beforeBackupRestore)
	evidence = append(evidence, []byte(backupCursor))
	credentialB := harness.IssueCredential(credentialA.Principal)
	assertPrincipalVersion(t, credentialB.Principal, contract.PrincipalActive, "3", "2", true)
	oldA := harness.ModernList(credentialA.Bearer, json.RawMessage(`"old-a"`), "")
	appendSnapshotEvidence(&evidence, oldA)
	assertAuthenticationProblem(t, oldA)
	currentB := harness.ModernList(credentialB.Bearer, json.RawMessage(`"current-b"`), "")
	appendSnapshotEvidence(&evidence, currentB)
	assertDiscoveryNamePage(t, currentB, visibleNames[:100], discoveryCursor(t, currentB))
	harness.DeleteGrant(grant.ID)
	deletedGrant := harness.adminSnapshot(http.MethodGet, "/api/v1/grants/"+grant.ID, nil)
	appendSnapshotEvidence(&evidence, deletedGrant)
	assertNotFoundProblem(t, deletedGrant)
	for _, name := range []string{"gateway.db", "gateway.db-wal", "gateway.db-shm"} {
		contents, readErr := os.ReadFile(filepath.Join(harness.root, name))
		require.NoError(t, readErr, name)
		evidence = append(evidence, contents)
	}

	initialAdmin := harness.bearer
	processResults = append(processResults, harness.Stop(syscall.SIGTERM))
	resetSecret := filepath.Join(t.TempDir(), "reset-admin")
	resetArgs := []string{"admin-reset", "--data-dir", harness.root, "--secret-output", resetSecret}
	appendArgumentEvidence(t, &evidence, resetArgs)
	resetResult, err := harness.runner.Run(harness.ctx, harness.binary, resetArgs...)
	require.NoError(t, err, string(resetResult.Stderr))
	processResults = append(processResults, resetResult)
	resetAdmin := readBearer(t, resetSecret)
	require.NotEqual(t, initialAdmin, resetAdmin)
	harness.bearer = resetAdmin
	harness.Start()
	waitForStdioServer(t, harness, catalog.ServerID, activeCatalog)
	oldInitialAdmin := harness.requestSnapshot(http.MethodGet, "/api/v1/system-status", nil, map[string]string{"Authorization": "Bearer " + initialAdmin})
	appendSnapshotEvidence(&evidence, oldInitialAdmin)
	assertAuthenticationProblem(t, oldInitialAdmin)
	assertPrincipalVersion(t, harness.GetPrincipal(principal.Resource.ID), contract.PrincipalActive, "3", "2", true)
	assertModernNames(t, harness, credentialB.Bearer, names, "b-after-admin-reset")
	stillDeleted := harness.adminSnapshot(http.MethodGet, "/api/v1/grants/"+grant.ID, nil)
	appendSnapshotEvidence(&evidence, stillDeleted)
	assertNotFoundProblem(t, stillDeleted)
	preRestore := harness.ModernList(credentialB.Bearer, json.RawMessage(`"pre-restore"`), "")
	appendSnapshotEvidence(&evidence, preRestore)
	preRestoreCursor := discoveryCursor(t, preRestore)
	evidence = append(evidence, []byte(preRestoreCursor))

	processResults = append(processResults, harness.Stop(syscall.SIGTERM))
	restoreSecret := filepath.Join(t.TempDir(), "restore-admin")
	restoreArgs := []string{"restore", artifact.ID, "--data-dir", harness.root, "--secret-output", restoreSecret, "--output", "json"}
	appendArgumentEvidence(t, &evidence, restoreArgs)
	restoreResult, err := harness.runner.Run(harness.ctx, harness.binary, restoreArgs...)
	require.NoError(t, err, string(restoreResult.Stderr))
	processResults = append(processResults, restoreResult)
	restoreAdmin := readBearer(t, restoreSecret)
	require.NotEqual(t, initialAdmin, restoreAdmin)
	require.NotEqual(t, resetAdmin, restoreAdmin)
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

	harness.bearer = restoreAdmin
	harness.Start()
	waitForStdioServer(t, harness, catalog.ServerID, activeCatalog)
	for _, oldAdmin := range []string{initialAdmin, resetAdmin} {
		response := harness.requestSnapshot(http.MethodGet, "/api/v1/system-status", nil, map[string]string{"Authorization": "Bearer " + oldAdmin})
		appendSnapshotEvidence(&evidence, response)
		assertAuthenticationProblem(t, response)
	}
	restoredPrincipal := harness.GetPrincipal(principal.Resource.ID)
	assertPrincipalVersion(t, restoredPrincipal, contract.PrincipalActive, "3", "2", false)
	restoredGrant := harness.GetGrant(grant.ID)
	assert.Equal(t, grant, restoredGrant)
	grants := harness.ListGrants(principal.Resource.ID, "")
	require.Len(t, grants, 2)
	assert.Contains(t, grants, grant)
	restoredA := harness.ModernList(credentialA.Bearer, json.RawMessage(`"restored-a"`), "")
	appendSnapshotEvidence(&evidence, restoredA)
	assertAuthenticationProblem(t, restoredA)
	restoredB := harness.ModernList(credentialB.Bearer, json.RawMessage(`"restored-b"`), "")
	appendSnapshotEvidence(&evidence, restoredB)
	assertAuthenticationProblem(t, restoredB)
	credentialC := harness.IssueCredential(restoredPrincipal)
	assertPrincipalVersion(t, credentialC.Principal, contract.PrincipalActive, "4", "3", true)
	currentC := harness.ModernList(credentialC.Bearer, json.RawMessage(`"current-c"`), "")
	appendSnapshotEvidence(&evidence, currentC)
	assertDiscoveryNamePage(t, currentC, visibleNames[:100], discoveryCursor(t, currentC))
	staleRestoredCursor := harness.ModernList(credentialC.Bearer, json.RawMessage(`"stale-restored-cursor"`), preRestoreCursor)
	appendSnapshotEvidence(&evidence, staleRestoredCursor)
	assertRPCError(t, staleRestoredCursor, `{"jsonrpc":"2.0","id":"stale-restored-cursor","error":{"code":-32001,"message":"The tools/list cursor is stale.","data":{"code":"stale_cursor"}}}`)

	fixtureEvidence, err := json.Marshal(struct {
		Events []httpFixtureEvent `json:"events"`
		Tools  []fixtureTool      `json:"tools"`
	}{Events: catalog.Fixture.Events(), Tools: fixtureTools(names)})
	require.NoError(t, err)
	evidence = append(evidence, fixtureEvidence, []byte(backupCursor), []byte(preRestoreCursor))
	processResults = append(processResults, harness.Stop(syscall.SIGTERM))
	scanRecoveryCanaries(t, []*agentBearer{credentialA.Bearer, credentialB.Bearer, credentialC.Bearer}, []string{initialAdmin, resetAdmin, restoreAdmin}, harness.root, evidence, processResults)
}

type boundedEventReader struct {
	reader io.Reader
}

func newBoundedEventReader(reader io.Reader) *boundedEventReader {
	return &boundedEventReader{reader: reader}
}

func (reader *boundedEventReader) frame(t *testing.T) []byte {
	t.Helper()
	const maximum = 4096
	var frame []byte
	one := make([]byte, 1)
	for len(frame) < maximum {
		_, err := io.ReadFull(reader.reader, one)
		require.NoError(t, err)
		frame = append(frame, one[0])
		if bytes.HasSuffix(frame, []byte("\n\n")) {
			return frame
		}
	}
	t.Fatal("SSE frame exceeded recovery evidence bound")
	return nil
}

func assertBackupArtifactModes(t *testing.T, root, backupID string) {
	t.Helper()
	directory := filepath.Join(root, "backups", backupID)
	info, err := os.Stat(directory)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	for _, name := range []string{"gateway.db", "metadata.json"} {
		info, err = os.Stat(filepath.Join(directory, name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func appendSnapshotEvidence(evidence *[][]byte, response responseSnapshot) {
	*evidence = append(*evidence, append([]byte(nil), response.Body...))
	headers, _ := json.Marshal(response.Header)
	*evidence = append(*evidence, headers)
}

func appendArgumentEvidence(t *testing.T, evidence *[][]byte, arguments ...[]string) {
	t.Helper()
	for _, values := range arguments {
		encoded, err := json.Marshal(values)
		require.NoError(t, err)
		*evidence = append(*evidence, encoded)
	}
}

func scanRecoveryCanaries(t *testing.T, agents []*agentBearer, admins []string, root string, evidence [][]byte, results []testutil.ProcessResult) {
	t.Helper()
	type namedScanner struct {
		name string
		scan func(string, io.Reader) error
	}
	scanners := make([]namedScanner, 0, len(agents)+len(admins))
	for index, bearer := range agents {
		scanners = append(scanners, namedScanner{name: "agent-" + strconv.Itoa(index), scan: bearer.scan})
	}
	for index, bearer := range admins {
		scanner, err := testutil.NewCanaryScanner([]byte(bearer))
		require.NoError(t, err)
		scanners = append(scanners, namedScanner{name: "admin-" + strconv.Itoa(index), scan: scanner.Scan})
	}
	for index, result := range results {
		require.False(t, result.StdoutTruncated, "process %d stdout was truncated", index)
		require.False(t, result.StderrTruncated, "process %d stderr was truncated", index)
	}
	for _, scanner := range scanners {
		for index, result := range results {
			require.NoError(t, scanner.scan(scanner.name+" process stdout "+strconv.Itoa(index), bytes.NewReader(result.Stdout)))
			require.NoError(t, scanner.scan(scanner.name+" process stderr "+strconv.Itoa(index), bytes.NewReader(result.Stderr)))
		}
		for index, value := range evidence {
			require.NoError(t, scanner.scan(scanner.name+" evidence "+strconv.Itoa(index), bytes.NewReader(value)))
		}
		require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || !info.Mode().IsRegular() {
				return walkErr
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			return errors.Join(scanner.scan(scanner.name+" data artifact", file), file.Close())
		}))
	}
}
