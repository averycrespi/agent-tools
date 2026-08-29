//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreAcceptedSchemaLineages(t *testing.T) {
	t.Run("schemas 3 through 9 migrate to an empty valid request store", func(t *testing.T) {
		for schema := 3; schema <= 9; schema++ {
			t.Run(strconv.Itoa(schema), func(t *testing.T) {
				ctx := context.Background()
				root := filepath.Join(t.TempDir(), "gateway")
				require.NoError(t, os.Mkdir(root, 0o700))
				ownership, err := gatewaypaths.Acquire(root)
				require.NoError(t, err)
				live, err := storage.Initialize(ctx, ownership, backupTestInstallationID)
				require.NoError(t, err)
				clock := fixedClock{value: acceptedFixtureTime}
				_, err = admin.NewService(live, clock, bytes.NewReader(bytes.Repeat([]byte{0xA1}, 256))).Initialize(ctx, new(captureSink))
				require.NoError(t, err)
				layout := ownership.Layout()
				require.NoError(t, live.Close())
				require.NoError(t, ownership.Close())

				fixtureRoot := filepath.Join(t.TempDir(), "fixture")
				fixturePath, err := storage.WriteAcceptedSchemaFixtureForIntegration(ctx, fixtureRoot, backupTestInstallationID, schema)
				require.NoError(t, err)
				fixture, err := sql.Open("sqlite3", "file:"+fixturePath+"?_pragma=busy_timeout(2000)")
				require.NoError(t, err)
				_, err = fixture.ExecContext(ctx, `INSERT INTO admin_credentials
					(id, verifier, fingerprint, created_at, expires_at, status, revision)
					VALUES (?, ?, ?, ?, NULL, 'active', 1)`,
					"01ARZ3NDEKTSV4RRFFQ69G5FAV", bytes.Repeat([]byte{0xA0}, 32), "0123456789abcdef",
					acceptedFixtureTime.Format(time.RFC3339Nano))
				require.NoError(t, err)
				_, err = fixture.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
				require.NoError(t, err)
				require.NoError(t, fixture.Close())
				backupID := fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G5F%02d", schema)
				installRestoreArtifact(t, layout, fixturePath, backupID)

				_, err = Restore(ctx, RestoreOptions{
					Root: root, BackupID: backupID, Sink: new(captureSink), Clock: clock,
					Entropy: bytes.NewReader(restoreTestEntropy(0xA2, 4096)),
				})
				require.NoError(t, err)
				ownership, err = gatewaypaths.Acquire(root)
				require.NoError(t, err)
				restored, err := storage.Open(ctx, ownership)
				require.NoError(t, err)
				targets, authority := restoreValidationOwners(t, restored, clock, 0xA3)
				require.NoError(t, authority.ValidateStartup(ctx, targets))
				require.NoError(t, grantrequests.ValidateStartup(ctx, restored, authority, targets))
				opened, err := restored.Identity(ctx)
				require.NoError(t, err)
				assert.Equal(t, storage.CurrentSchema, opened.SchemaVersion)
				assertRestoreRequestCounts(t, restored, 0, 0, 0)
				require.NoError(t, restored.Close())
				require.NoError(t, ownership.Close())
			})
		}
	})

	t.Run("schema 10 preserves requests evidence and historical grant", func(t *testing.T) {
		ctx := context.Background()
		root := filepath.Join(t.TempDir(), "gateway")
		require.NoError(t, os.Mkdir(root, 0o700))
		ownership, err := gatewaypaths.Acquire(root)
		require.NoError(t, err)
		store, err := storage.Initialize(ctx, ownership, backupTestInstallationID)
		require.NoError(t, err)
		clock := fixedClock{value: acceptedFixtureTime}
		_, err = admin.NewService(store, clock, bytes.NewReader(restoreTestEntropy(0xB0, 1024))).Initialize(ctx, new(captureSink))
		require.NoError(t, err)
		targets, err := servers.New(store, clock, bytes.NewReader(restoreTestEntropy(0xB1, 4096)))
		require.NoError(t, err)
		requestHash := sha256.Sum256([]byte("restore-target"))
		createdServer, err := targets.Create(ctx, servers.CreateRequest{Definition: servers.Definition{
			Namespace: "sample", DisplayName: "Sample", Enabled: false,
			Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}},
		}, Idempotency: &servers.IdempotencyRequest{AuthorityID: backupTestInstallationID, Method: "POST", Route: "/api/v1/servers", Key: "restore-target", RequestHash: requestHash}})
		require.NoError(t, err)
		authority, err := authorization.New(store, clock, bytes.NewReader(restoreTestEntropy(0xB2, 4096)))
		require.NoError(t, err)
		principal, err := authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "Restore owner", Visibility: contract.VisibilityRequestable})
		require.NoError(t, err)
		backupCredential, err := authority.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
		require.NoError(t, err)
		descriptor := restoreDescriptor(t, createdServer.Server.ID)
		descriptors := restoreDescriptorInspector{descriptor: descriptor}
		requests, err := grantrequests.New(grantrequests.Options{
			Store: store, Clock: clock, Entropy: bytes.NewReader(restoreTestEntropy(0xB3, 4096)),
			Namespaces: targets, Descriptors: descriptors, Denies: authority, Invalidate: func(contract.Invalidation) {},
		})
		require.NoError(t, err)
		toolResult, err := requests.CreateOrExisting(ctx, grantrequests.CreateRequest{PrincipalID: principal.Principal.ID, Policy: contract.Policy{Scope: contract.PolicyTool, Target: "sample.echo"}})
		require.NoError(t, err)
		serverResult, err := requests.CreateOrExisting(ctx, grantrequests.CreateRequest{PrincipalID: principal.Principal.ID, Policy: contract.Policy{Scope: contract.PolicyServer, Target: "sample", FutureToolsAcknowledged: true}})
		require.NoError(t, err)
		grant, err := authority.CreateGrant(ctx, authorization.CreateGrantRequest{PrincipalID: principal.Principal.ID, Effect: contract.GrantAllow, ServerID: createdServer.Server.ID}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
		require.NoError(t, err)
		approvedAt := acceptedFixtureTime.Add(time.Second)
		closedAt := approvedAt.Format("2006-01-02T15:04:05.000000000Z07:00")
		_, approvedEvidence, err := grantrequests.BuildDescriptorEvidence(descriptor, "sample", approvedAt)
		require.NoError(t, err)
		require.NoError(t, store.Mutate(ctx, func(transaction *sql.Tx) error {
			_, updateErr := transaction.ExecContext(ctx, `UPDATE grant_requests SET
				state = 'approved', revision = 2, approved_scope = 'tool', approved_target = 'sample.echo',
				approved_future_tools_acknowledged = 0, approved_grant_id = ?, approved_evidence = ?, updated_at = ?, closed_at = ?
				WHERE id = ?`, grant.ID, approvedEvidence, closedAt, closedAt, serverResult.Request.ID)
			return updateErr
		}))
		require.NoError(t, authority.DeleteGrant(ctx, grant.ID))
		require.NoError(t, requests.ValidateStartup(ctx, authority, targets))
		beforeTool, err := requests.GetAdmin(ctx, toolResult.Request.ID)
		require.NoError(t, err)
		beforeServer, err := requests.GetAdmin(ctx, serverResult.Request.ID)
		require.NoError(t, err)
		beforeEvidence := requestEvidence(t, store, toolResult.Request.ID)
		beforeApprovedEvidence := requestApprovedEvidence(t, store, serverResult.Request.ID)
		require.NotEmpty(t, beforeEvidence)
		require.NotEmpty(t, beforeApprovedEvidence)
		assertRestoreRequestCounts(t, store, 2, 2, int64(len(beforeEvidence)+len(beforeApprovedEvidence)))
		manager, err := New(Options{Store: store, Layout: ownership.Layout(), Clock: clock, Entropy: bytes.NewReader(restoreTestEntropy(0xB4, 1024))})
		require.NoError(t, err)
		artifact, _, err := manager.Create(ctx, "authority", "s5-restore")
		require.NoError(t, err)
		malformedID := "01ARZ3NDEKTSV4RRFFQ69G5FA1"
		cloneMalformedRestoreArtifact(t, ownership.Layout(), artifact.ID, malformedID, principal.Principal.ID, createdServer.Server.ID)
		currentCredential, err := authority.IssueCredential(ctx, backupCredential.Principal.ID, backupCredential.Principal.Revision)
		require.NoError(t, err)
		databasePath := ownership.Layout().Database
		require.NoError(t, store.Close())
		require.NoError(t, ownership.Close())
		activeDigest, err := digestFile(databasePath)
		require.NoError(t, err)

		malformedSink := new(captureSink)
		_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: malformedID, Sink: malformedSink, Clock: clock, Entropy: bytes.NewReader(restoreTestEntropy(0xB5, 4096))})
		require.ErrorIs(t, err, grantrequests.ErrInvalidState)
		assert.Empty(t, malformedSink.bearer)
		afterFailureDigest, digestErr := digestFile(filepath.Join(root, gatewaypaths.DatabaseName))
		require.NoError(t, digestErr)
		assert.Equal(t, activeDigest, afterFailureDigest)
		for _, suffix := range []string{".restore", ".restore-wal", ".restore-shm", ".restore.mutation", ".restore.mutation.tmp", ".restore.mutation.cleared"} {
			_, statErr := os.Lstat(filepath.Join(root, gatewaypaths.DatabaseName) + suffix)
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		}
		ownership, err = gatewaypaths.Acquire(root)
		require.NoError(t, err)
		active, err := storage.Open(ctx, ownership)
		require.NoError(t, err)
		activeAuthority, err := authorization.New(active, clock, bytes.NewReader(restoreTestEntropy(0xB6, 1024)))
		require.NoError(t, err)
		lease, err := activeAuthority.Authenticate(ctx, currentCredential.Bearer)
		require.NoError(t, err)
		lease.Release()
		require.NoError(t, active.Close())
		require.NoError(t, ownership.Close())

		restoredSink := new(captureSink)
		_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: artifact.ID, Sink: restoredSink, Clock: clock, Entropy: bytes.NewReader(restoreTestEntropy(0xB7, 4096))})
		require.NoError(t, err)
		ownership, err = gatewaypaths.Acquire(root)
		require.NoError(t, err)
		restored, err := storage.Open(ctx, ownership)
		require.NoError(t, err)
		restoredTargets, restoredAuthority := restoreValidationOwners(t, restored, clock, 0xB8)
		require.NoError(t, restoredAuthority.ValidateStartup(ctx, restoredTargets))
		require.NoError(t, grantrequests.ValidateStartup(ctx, restored, restoredAuthority, restoredTargets))
		_, err = restoredAuthority.Authenticate(ctx, backupCredential.Bearer)
		assert.ErrorIs(t, err, authorization.ErrAuthenticationRequired)
		_, err = restoredAuthority.Authenticate(ctx, currentCredential.Bearer)
		assert.ErrorIs(t, err, authorization.ErrAuthenticationRequired)
		restoredRequests, err := grantrequests.New(grantrequests.Options{
			Store: restored, Clock: clock, Entropy: bytes.NewReader(restoreTestEntropy(0xB9, 1024)),
			Namespaces: restoredTargets, Descriptors: descriptors, Denies: restoredAuthority, Invalidate: func(contract.Invalidation) {},
		})
		require.NoError(t, err)
		afterTool, err := restoredRequests.GetAdmin(ctx, toolResult.Request.ID)
		require.NoError(t, err)
		afterServer, err := restoredRequests.GetAdmin(ctx, serverResult.Request.ID)
		require.NoError(t, err)
		assert.Equal(t, beforeTool, afterTool)
		assert.Equal(t, beforeServer, afterServer)
		assert.Equal(t, beforeEvidence, requestEvidence(t, restored, toolResult.Request.ID))
		assert.Equal(t, beforeApprovedEvidence, requestApprovedEvidence(t, restored, serverResult.Request.ID))
		assert.Equal(t, contract.RequestApproved, afterServer.State)
		require.NotNil(t, afterServer.ApprovedGrantID)
		assert.Equal(t, grant.ID, *afterServer.ApprovedGrantID)
		var liveGrantCount int64
		require.NoError(t, restored.View(ctx, func(transaction *sql.Tx) error {
			return transaction.QueryRowContext(ctx, `SELECT count(*) FROM grants WHERE id = ?`, grant.ID).Scan(&liveGrantCount)
		}))
		assert.Zero(t, liveGrantCount)
		assertRestoreRequestCounts(t, restored, 2, 2, int64(len(beforeEvidence)+len(beforeApprovedEvidence)))
		require.NoError(t, restored.Close())
		require.NoError(t, ownership.Close())
	})
}

type restoreDescriptorInspector struct{ descriptor catalog.DurableDescriptor }

func (inspector restoreDescriptorInspector) LookupDurableDescriptorTx(context.Context, *sql.Tx, string, string) (catalog.DurableDescriptor, error) {
	return inspector.descriptor, nil
}

func restoreDescriptor(t *testing.T, serverID string) catalog.DurableDescriptor {
	t.Helper()
	normalized, err := catalog.NormalizeTool(catalog.RawTool{
		UpstreamName: "echo", ExternalName: "sample.echo",
		Descriptor: json.RawMessage(`{"name":"echo","description":"Echo","inputSchema":{"type":"object","additionalProperties":false}}`),
	}, catalog.NormalizeOptions{ServerID: serverID})
	require.NoError(t, err)
	return catalog.DurableDescriptor{Resource: contract.ToolDescriptor{
		ID: "01ARZ3NDEKTSV4RRFFQ69G5FA2", ServerID: serverID, UpstreamName: normalized.Key.UpstreamName,
		ExternalName: normalized.ExternalName, Descriptor: normalized.Descriptor, Fingerprint: normalized.Fingerprint, CatalogRevision: "1",
	}, State: contract.EvidenceCurrent}
}

func restoreValidationOwners(t *testing.T, store *storage.Store, clock fixedClock, seed byte) (*servers.Repository, *authorization.Repository) {
	t.Helper()
	targets, err := servers.New(store, clock, bytes.NewReader(restoreTestEntropy(seed, 2048)))
	require.NoError(t, err)
	authority, err := authorization.New(store, clock, bytes.NewReader(restoreTestEntropy(seed+1, 2048)))
	require.NoError(t, err)
	return targets, authority
}

func installRestoreArtifact(t *testing.T, layout gatewaypaths.Layout, source, backupID string) {
	t.Helper()
	artifactDir := filepath.Join(layout.Backups, backupID)
	require.NoError(t, os.MkdirAll(artifactDir, 0o700))
	artifactPath := filepath.Join(artifactDir, databaseFile)
	require.NoError(t, copyOwnerOnly(source, artifactPath))
	identity, err := storage.VerifyBackup(context.Background(), artifactPath)
	require.NoError(t, err)
	info, err := os.Stat(artifactPath)
	require.NoError(t, err)
	digest, err := digestFile(artifactPath)
	require.NoError(t, err)
	require.NoError(t, writeMetadata(filepath.Join(artifactDir, metadataFile), artifactMetadata{
		Backup: contract.Backup{
			ID: backupID, CreatedAt: acceptedFixtureTime.Format(time.RFC3339Nano), InstallationID: identity.InstallationID,
			SchemaVersion: strconv.Itoa(identity.SchemaVersion), SourceRevision: strconv.FormatUint(identity.Revision, 10),
			SizeBytes: info.Size(), SHA256: digest,
		},
		AuthorityHash: digestText("authority"), KeyHash: digestText("key"), InputHash: digestText("{}"),
	}))
}

func cloneMalformedRestoreArtifact(t *testing.T, layout gatewaypaths.Layout, sourceID, targetID, principalID, serverID string) {
	t.Helper()
	sourceDir := filepath.Join(layout.Backups, sourceID)
	targetDir := filepath.Join(layout.Backups, targetID)
	require.NoError(t, os.MkdirAll(targetDir, 0o700))
	targetDB := filepath.Join(targetDir, databaseFile)
	require.NoError(t, copyOwnerOnly(filepath.Join(sourceDir, databaseFile), targetDB))
	database, err := sql.Open("sqlite3", "file:"+targetDB+"?_pragma=busy_timeout(2000)")
	require.NoError(t, err)
	createdAt := acceptedFixtureTime.Format("2006-01-02T15:04:05.000000000Z07:00")
	malformedRequestID := "01ARZ3NDEKTSV4RRFFQ69G5FA3"
	_, err = database.Exec(`INSERT INTO grant_request_identities (id, created_at) VALUES (?, ?)`, malformedRequestID, createdAt)
	require.NoError(t, err)
	_, err = database.Exec(`INSERT INTO grant_requests (
		id, principal_id, state, revision, resolved_server_id, resolved_upstream_name,
		requested_scope, requested_target, requested_constraint, requested_duration_seconds,
		requested_future_tools_acknowledged, dedupe_version, dedupe_bytes, submitted_evidence,
		approved_scope, approved_target, approved_constraint, approved_duration_seconds,
		approved_future_tools_acknowledged, approved_grant_id, rejection_reason, approved_evidence,
		created_at, updated_at, closed_at
	) VALUES (?, ?, 'pending', 1, ?, NULL, 'server', 'sample', NULL, NULL, 1, 1, X'00', NULL,
		NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?, NULL)`, malformedRequestID, principalID, serverID, createdAt, createdAt)
	require.NoError(t, err)
	_, err = database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())
	metadataBytes, err := os.ReadFile(filepath.Join(sourceDir, metadataFile))
	require.NoError(t, err)
	var metadata artifactMetadata
	require.NoError(t, json.Unmarshal(metadataBytes, &metadata))
	metadata.Backup.ID = targetID
	info, err := os.Stat(targetDB)
	require.NoError(t, err)
	metadata.Backup.SizeBytes = info.Size()
	metadata.Backup.SHA256, err = digestFile(targetDB)
	require.NoError(t, err)
	require.NoError(t, writeMetadata(filepath.Join(targetDir, metadataFile), metadata))
	_, err = storage.VerifyBackup(context.Background(), targetDB)
	require.NoError(t, err)
}

func requestEvidence(t *testing.T, store *storage.Store, requestID string) []byte {
	t.Helper()
	var evidence []byte
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT submitted_evidence FROM grant_requests WHERE id = ?`, requestID).Scan(&evidence)
	}))
	return evidence
}

func requestApprovedEvidence(t *testing.T, store *storage.Store, requestID string) []byte {
	t.Helper()
	var evidence []byte
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT approved_evidence FROM grant_requests WHERE id = ?`, requestID).Scan(&evidence)
	}))
	return evidence
}

func assertRestoreRequestCounts(t *testing.T, store *storage.Store, identities, requests, evidence int64) {
	t.Helper()
	var actualIdentities, actualRequests, actualEvidence int64
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		if err := transaction.QueryRow(`SELECT count(*) FROM grant_request_identities`).Scan(&actualIdentities); err != nil {
			return err
		}
		if err := transaction.QueryRow(`SELECT count(*) FROM grant_requests`).Scan(&actualRequests); err != nil {
			return err
		}
		return transaction.QueryRow(`SELECT total_bytes FROM grant_request_evidence_bytes WHERE singleton = 1`).Scan(&actualEvidence)
	}))
	assert.Equal(t, identities, actualIdentities)
	assert.Equal(t, requests, actualRequests)
	assert.Equal(t, evidence, actualEvidence)
}
