package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var acceptedFixtureTime = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

type captureSink struct{ bearer string }

func (sink *captureSink) Publish(value string) error {
	sink.bearer = value
	return nil
}

func TestRestoreReplacesCompleteGenerationAndRekeysAdminAuthority(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, backupTestInstallationID)
	require.NoError(t, err)
	clock := fixedClock{value: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)}
	initialSink := new(captureSink)
	_, err = admin.NewService(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x11}, 256))).Initialize(ctx, initialSink)
	require.NoError(t, err)
	manager, err := New(Options{Store: store, Layout: ownership.Layout(), Clock: clock, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x22}, 256))})
	require.NoError(t, err)
	artifact, _, err := manager.Create(ctx, "authority", "restore-fixture")
	require.NoError(t, err)
	artifactBytes, err := os.ReadFile(filepath.Join(ownership.Layout().Backups, artifact.ID, databaseFile))
	require.NoError(t, err)
	assert.NotContains(t, string(artifactBytes), initialSink.bearer)
	laterSink := new(captureSink)
	_, err = admin.NewService(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x33}, 256))).Reset(ctx, laterSink)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	for _, suffix := range []string{"-wal", "-shm"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, gatewaypaths.DatabaseName)+suffix, []byte("stale"), 0o600))
	}
	replacementSink := new(captureSink)
	identity, err := Restore(ctx, RestoreOptions{
		Root: root, BackupID: artifact.ID, Sink: replacementSink, Clock: clock,
		Entropy: bytes.NewReader(bytes.Repeat([]byte{0x44}, 256)),
	})
	require.NoError(t, err)
	assert.Equal(t, backupTestInstallationID, identity.InstallationID)
	assert.NotEmpty(t, replacementSink.bearer)
	for _, suffix := range []string{"-wal", "-shm"} {
		_, statErr := os.Lstat(filepath.Join(root, gatewaypaths.DatabaseName) + suffix)
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	}

	ownership, err = gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err = storage.Open(ctx, ownership)
	require.NoError(t, err)
	service := admin.NewService(store, clock, bytes.NewReader(nil))
	_, err = service.Authenticate(ctx, replacementSink.bearer)
	require.NoError(t, err)
	_, err = service.Authenticate(ctx, initialSink.bearer)
	assert.ErrorIs(t, err, admin.ErrAuthenticationRequired)
	_, err = service.Authenticate(ctx, laterSink.bearer)
	assert.ErrorIs(t, err, admin.ErrAuthenticationRequired)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
}

func TestRestoreInvalidatesAgentCredentialsAndAllowsFreshIssuance(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, backupTestInstallationID)
	require.NoError(t, err)
	clock := fixedClock{value: acceptedFixtureTime}
	initialAdmin := new(captureSink)
	_, err = admin.NewService(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x81}, 256))).Initialize(ctx, initialAdmin)
	require.NoError(t, err)
	authority, err := authorization.New(store, clock, bytes.NewReader(restoreTestEntropy(0x82, 4096)))
	require.NoError(t, err)
	created, err := authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{
		DisplayName: "restored agent",
		Visibility:  contract.VisibilityAll,
	})
	require.NoError(t, err)
	issued, err := authority.IssueCredential(ctx, created.Principal.ID, created.Principal.Revision)
	require.NoError(t, err)
	backupBearer := issued.Bearer
	secondCreated, err := authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{
		DisplayName: "second restored agent",
		Visibility:  contract.VisibilityAllowedOnly,
	})
	require.NoError(t, err)
	secondIssued, err := authority.IssueCredential(ctx, secondCreated.Principal.ID, secondCreated.Principal.Revision)
	require.NoError(t, err)
	policyRevision, err := authority.AuthorizationRevision(ctx)
	require.NoError(t, err)
	policy, err := authority.ListGrants(ctx, authorization.GrantFilter{}, nil, 100)
	require.NoError(t, err)
	manager, err := New(Options{Store: store, Layout: ownership.Layout(), Clock: clock, Entropy: bytes.NewReader(restoreTestEntropy(0x83, 1024))})
	require.NoError(t, err)
	artifact, _, err := manager.Create(ctx, "authority", "credential-restore")
	require.NoError(t, err)
	current, err := authority.IssueCredential(ctx, issued.Principal.ID, issued.Principal.Revision)
	require.NoError(t, err)
	currentBearer := current.Bearer
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	restoredAdmin := new(captureSink)
	_, err = Restore(ctx, RestoreOptions{
		Root: root, BackupID: artifact.ID, Sink: restoredAdmin, Clock: clock,
		Entropy: bytes.NewReader(restoreTestEntropy(0x84, 4096)),
	})
	require.NoError(t, err)

	ownership, err = gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err = storage.Open(ctx, ownership)
	require.NoError(t, err)
	authority, err = authorization.New(store, clock, bytes.NewReader(restoreTestEntropy(0x85, 4096)))
	require.NoError(t, err)
	_, err = authority.Authenticate(ctx, backupBearer)
	assert.ErrorIs(t, err, authorization.ErrAuthenticationRequired)
	_, err = authority.Authenticate(ctx, currentBearer)
	assert.ErrorIs(t, err, authorization.ErrAuthenticationRequired)
	_, err = authority.Authenticate(ctx, secondIssued.Bearer)
	assert.ErrorIs(t, err, authorization.ErrAuthenticationRequired)

	restoredPrincipal, err := authority.GetPrincipal(ctx, issued.Principal.ID)
	require.NoError(t, err)
	assert.Nil(t, restoredPrincipal.Credential)
	assert.Equal(t, "3", restoredPrincipal.Revision)
	assert.Equal(t, "2", restoredPrincipal.CredentialRevision)
	restoredSecond, err := authority.GetPrincipal(ctx, secondIssued.Principal.ID)
	require.NoError(t, err)
	assert.Nil(t, restoredSecond.Credential)
	assert.Equal(t, "3", restoredSecond.Revision)
	assert.Equal(t, "2", restoredSecond.CredentialRevision)
	restoredPolicyRevision, err := authority.AuthorizationRevision(ctx)
	require.NoError(t, err)
	assert.Equal(t, policyRevision, restoredPolicyRevision)
	restoredPolicy, err := authority.ListGrants(ctx, authorization.GrantFilter{}, nil, 100)
	require.NoError(t, err)
	assert.Equal(t, policy, restoredPolicy)
	fresh, err := authority.IssueCredential(ctx, restoredPrincipal.ID, restoredPrincipal.Revision)
	require.NoError(t, err)
	assert.NotEqual(t, backupBearer, fresh.Bearer)
	assert.NotEqual(t, currentBearer, fresh.Bearer)
	lease, err := authority.Authenticate(ctx, fresh.Bearer)
	require.NoError(t, err)
	lease.Release()

	resetAdmin := new(captureSink)
	_, err = admin.NewService(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x86}, 256))).Reset(ctx, resetAdmin)
	require.NoError(t, err)
	_, err = admin.NewService(store, clock, bytes.NewReader(nil)).Authenticate(ctx, restoredAdmin.bearer)
	assert.ErrorIs(t, err, admin.ErrAuthenticationRequired)
	lease, err = authority.Authenticate(ctx, fresh.Bearer)
	require.NoError(t, err)
	lease.Release()
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
}

func restoreTestEntropy(seed byte, size int) []byte {
	entropy := make([]byte, size)
	for index := range entropy {
		entropy[index] = seed + byte(index%251)
	}
	return entropy
}

func TestRestorePreservesS2AuthorityAndInterruptsWorkBeforeReconstruction(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, backupTestInstallationID)
	require.NoError(t, err)
	clock := fixedClock{value: acceptedFixtureTime}
	_, err = admin.NewService(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x50}, 1024))).Initialize(ctx, new(captureSink))
	require.NoError(t, err)
	repository, err := serverdomain.New(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x51}, 1024)))
	require.NoError(t, err)
	requestHash := sha256.Sum256([]byte("s2-restore"))
	created, err := repository.Create(ctx, serverdomain.CreateRequest{Definition: serverdomain.Definition{Namespace: "restored", DisplayName: "Restored", Enabled: true, Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}}, Idempotency: &serverdomain.IdempotencyRequest{AuthorityID: backupTestInstallationID, Method: "POST", Route: "/api/v1/servers", Key: "s2-restore-key", RequestHash: requestHash}})
	require.NoError(t, err)
	require.NotNil(t, created.Operation)
	backupManager, err := New(Options{Store: store, Layout: ownership.Layout(), Clock: clock, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x52}, 1024))})
	require.NoError(t, err)
	artifact, _, err := backupManager.Create(ctx, "authority", "s2-restore")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: artifact.ID, Sink: new(captureSink), Clock: clock, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x53}, 1024))})
	require.NoError(t, err)
	ownership, err = gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err = storage.Open(ctx, ownership)
	require.NoError(t, err)
	repository, err = serverdomain.New(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x54}, 1024)))
	require.NoError(t, err)
	restored, err := repository.Get(ctx, created.Server.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.DesiredServerEnabled, restored.DesiredState)
	operation, err := repository.GetOperation(ctx, created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.OperationScheduled, operation.State)

	runtimeManager, err := runtimes.New(runtimes.Options{Repository: repository})
	require.NoError(t, err)
	require.NoError(t, runtimeManager.Start(ctx))
	runtimeManager.Shutdown()
	operation, err = repository.GetOperation(ctx, created.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.OperationInterrupted, operation.State)
	assert.NotEqual(t, contract.RuntimeActive, runtimeManager.Status(created.Server.ID).State)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
}

func TestRestoreMigratesAcceptedS1SchemaThreeBackupBeforePublication(t *testing.T) {
	ctx := context.Background()
	root, originalBearer := installAcceptedS1BackupFixture(t)
	replacementSink := new(captureSink)
	identity, err := Restore(ctx, RestoreOptions{
		Root: root, BackupID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Sink: replacementSink,
		Clock: fixedClock{acceptedFixtureTime}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x71}, 256)),
	})
	require.NoError(t, err)
	assert.Equal(t, storage.CurrentSchema, identity.SchemaVersion)
	assert.NotEmpty(t, replacementSink.bearer)

	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Open(ctx, ownership)
	require.NoError(t, err)
	opened, err := store.Identity(ctx)
	require.NoError(t, err)
	assert.Equal(t, storage.CurrentSchema, opened.SchemaVersion)
	service := admin.NewService(store, fixedClock{acceptedFixtureTime}, bytes.NewReader(nil))
	_, err = service.Authenticate(ctx, replacementSink.bearer)
	require.NoError(t, err)
	_, err = service.Authenticate(ctx, originalBearer)
	assert.ErrorIs(t, err, admin.ErrAuthenticationRequired)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
}

func TestAcceptedS1RestoreCrashPointsLeaveCurrentGenerationAuthoritative(t *testing.T) {
	for _, point := range []restoreFaultPoint{
		restoreFaultAfterCopy,
		restoreFaultAfterMigration,
		restoreFaultAfterRekey,
		restoreFaultBeforeInstall,
	} {
		t.Run(string(point), func(t *testing.T) {
			root, originalBearer := installAcceptedS1BackupFixture(t)
			sink := new(captureSink)
			_, err := Restore(context.Background(), RestoreOptions{
				Root: root, BackupID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Sink: sink,
				Clock: fixedClock{acceptedFixtureTime}, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x72}, 256)),
				fault: func(actual restoreFaultPoint) error {
					if actual == point {
						return assert.AnError
					}
					return nil
				},
			})
			require.Error(t, err)

			ownership, acquireErr := gatewaypaths.Acquire(root)
			require.NoError(t, acquireErr)
			store, openErr := storage.Open(context.Background(), ownership)
			require.NoError(t, openErr)
			service := admin.NewService(store, fixedClock{acceptedFixtureTime}, bytes.NewReader(nil))
			_, authErr := service.Authenticate(context.Background(), originalBearer)
			require.NoError(t, authErr)
			require.NoError(t, store.Close())
			require.NoError(t, ownership.Close())
			_, statErr := os.Lstat(filepath.Join(root, gatewaypaths.DatabaseName) + ".restore")
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func TestRestorePostInvalidationFaultsLeaveCurrentAgentAuthority(t *testing.T) {
	for _, point := range []restoreFaultPoint{
		restoreFaultAfterInvalidation,
		restoreFaultAfterRekey,
		restoreFaultAfterCheckpoint,
		restoreFaultBeforeInstall,
	} {
		t.Run(string(point), func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "gateway")
			require.NoError(t, os.Mkdir(root, 0o700))
			ownership, err := gatewaypaths.Acquire(root)
			require.NoError(t, err)
			store, err := storage.Initialize(ctx, ownership, backupTestInstallationID)
			require.NoError(t, err)
			clock := fixedClock{value: acceptedFixtureTime}
			_, err = admin.NewService(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x91}, 256))).Initialize(ctx, new(captureSink))
			require.NoError(t, err)
			authority, err := authorization.New(store, clock, bytes.NewReader(restoreTestEntropy(0x92, 4096)))
			require.NoError(t, err)
			created, err := authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "live agent", Visibility: contract.VisibilityAll})
			require.NoError(t, err)
			backedUp, err := authority.IssueCredential(ctx, created.Principal.ID, created.Principal.Revision)
			require.NoError(t, err)
			manager, err := New(Options{Store: store, Layout: ownership.Layout(), Clock: clock, Entropy: bytes.NewReader(restoreTestEntropy(0x93, 1024))})
			require.NoError(t, err)
			artifact, _, err := manager.Create(ctx, "authority", "restore-fault")
			require.NoError(t, err)
			current, err := authority.IssueCredential(ctx, backedUp.Principal.ID, backedUp.Principal.Revision)
			require.NoError(t, err)
			require.NoError(t, store.Close())
			require.NoError(t, ownership.Close())

			replacementAdmin := new(captureSink)
			_, err = Restore(ctx, RestoreOptions{
				Root: root, BackupID: artifact.ID, Sink: replacementAdmin, Clock: clock,
				Entropy: bytes.NewReader(restoreTestEntropy(0x94, 4096)),
				fault: func(actual restoreFaultPoint) error {
					if actual == point {
						return assert.AnError
					}
					return nil
				},
			})
			require.Error(t, err)

			ownership, err = gatewaypaths.Acquire(root)
			require.NoError(t, err)
			store, err = storage.Open(ctx, ownership)
			require.NoError(t, err)
			authority, err = authorization.New(store, clock, bytes.NewReader(restoreTestEntropy(0x95, 4096)))
			require.NoError(t, err)
			adminAuthority := admin.NewService(store, clock, bytes.NewReader(nil))
			if replacementAdmin.bearer != "" {
				_, err = adminAuthority.Authenticate(ctx, replacementAdmin.bearer)
				assert.ErrorIs(t, err, admin.ErrAuthenticationRequired)
			}
			lease, err := authority.Authenticate(ctx, current.Bearer)
			require.NoError(t, err)
			lease.Release()
			_, err = authority.Authenticate(ctx, backedUp.Bearer)
			assert.ErrorIs(t, err, authorization.ErrAuthenticationRequired)
			require.NoError(t, store.Close())
			require.NoError(t, ownership.Close())
			_, statErr := os.Lstat(filepath.Join(root, gatewaypaths.DatabaseName) + ".restore")
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func installAcceptedS1BackupFixture(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, backupTestInstallationID)
	require.NoError(t, err)
	sink := new(captureSink)
	_, err = admin.NewService(store, fixedClock{acceptedFixtureTime}, bytes.NewReader(bytes.Repeat([]byte{0x70}, 256))).Initialize(context.Background(), sink)
	require.NoError(t, err)
	layout := ownership.Layout()
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	fixture := filepath.Join("testdata", "01ARZ3NDEKTSV4RRFFQ69G5FAW")
	target := filepath.Join(layout.Backups, "01ARZ3NDEKTSV4RRFFQ69G5FAW")
	require.NoError(t, os.MkdirAll(target, 0o700))
	for _, name := range []string{databaseFile, metadataFile} {
		contents, readErr := os.ReadFile(filepath.Join(fixture, name))
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(filepath.Join(target, name), contents, 0o600))
	}
	return root, sink.bearer
}

func TestRestoreOrchestratesAuthorizationWithoutOwningS3SQL(t *testing.T) {
	source, err := os.ReadFile("restore.go")
	require.NoError(t, err)
	text := string(source)
	assert.Contains(t, text, "authorization.InvalidateStagedCredentials")
	assert.Contains(t, text, "authority.ValidateStartup")
	for _, forbidden := range []string{"UPDATE principals", "INSERT INTO principals", "DELETE FROM grants", "UPDATE authorization_meta"} {
		assert.False(t, strings.Contains(text, forbidden), forbidden)
	}
}

func TestRestoreRefusesRunningOrTamperedArtifact(t *testing.T) {
	ctx := context.Background()
	manager, store, ownership := newBackupManager(t, nil)
	artifact, _, err := manager.Create(ctx, "authority", "restore-fixture")
	require.NoError(t, err)
	_, err = Restore(ctx, RestoreOptions{Root: ownership.Layout().Root, BackupID: artifact.ID, Sink: new(captureSink), Clock: fixedClock{time.Now()}, Entropy: bytes.NewReader(bytes.Repeat([]byte{1}, 256))})
	assert.ErrorIs(t, err, gatewaypaths.ErrInUse)

	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
	file, err := os.OpenFile(filepath.Join(ownership.Layout().Backups, artifact.ID, databaseFile), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.Write([]byte("tamper"))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	_, err = Restore(ctx, RestoreOptions{Root: ownership.Layout().Root, BackupID: artifact.ID, Sink: new(captureSink), Clock: fixedClock{time.Now()}, Entropy: bytes.NewReader(bytes.Repeat([]byte{1}, 256))})
	assert.ErrorIs(t, err, ErrInvalidArtifact)
}
