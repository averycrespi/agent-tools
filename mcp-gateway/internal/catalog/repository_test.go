package catalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const catalogInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var catalogTime = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

type catalogClock struct{ now time.Time }

func (clock *catalogClock) Now() time.Time { return clock.now }

type catalogEntropy struct{ value uint64 }

func (reader *catalogEntropy) Read(target []byte) (int, error) {
	reader.value++
	for index := range target {
		target[index] = byte(reader.value >> (8 * (index % 8)))
	}
	return len(target), nil
}

func TestRepositoryCommitsRevisionsRetiresAndReusesImmutableIdentity(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	fence := catalogFence(server.ID, "0")
	first, err := repository.Commit(context.Background(), fence, candidateFor(t, server.ID, "sample", "one", "two"))
	require.NoError(t, err)
	assert.Equal(t, contract.DurableCatalogCurrent, first.State)
	assert.Equal(t, "1", *first.Revision)
	assert.Equal(t, int64(2), first.ToolCount)
	firstOne, err := descriptorByName(context.Background(), repository, server.ID, "one")
	require.NoError(t, err)

	clock.now = clock.now.Add(time.Minute)
	unchanged, err := repository.Commit(context.Background(), catalogFence(server.ID, "1"), candidateFor(t, server.ID, "sample", "one", "two"))
	require.NoError(t, err)
	assert.Equal(t, "2", *unchanged.Revision)
	unchangedOne, err := descriptorByName(context.Background(), repository, server.ID, "one")
	require.NoError(t, err)
	assert.Equal(t, firstOne.ID, unchangedOne.ID)
	assert.Equal(t, firstOne.FirstSeenAt, unchangedOne.FirstSeenAt)
	assert.NotEqual(t, firstOne.LastSeenAt, unchangedOne.LastSeenAt)

	clock.now = clock.now.Add(time.Minute)
	_, err = repository.Commit(context.Background(), catalogFence(server.ID, "2"), candidateFor(t, server.ID, "sample", "two", "three"))
	require.NoError(t, err)
	retiredOne, err := repository.GetDescriptor(context.Background(), server.ID, firstOne.ID)
	require.NoError(t, err)
	require.NotNil(t, retiredOne.RetiredAt)
	assert.Equal(t, "3", retiredOne.CatalogRevision)

	clock.now = clock.now.Add(time.Minute)
	empty, err := repository.Commit(context.Background(), catalogFence(server.ID, "3"), NormalizedCandidate{})
	require.NoError(t, err)
	assert.Equal(t, contract.DurableCatalogCurrent, empty.State)
	assert.Equal(t, int64(0), empty.ToolCount)
	assert.Equal(t, "4", *empty.Revision)

	clock.now = clock.now.Add(time.Minute)
	_, err = repository.Commit(context.Background(), catalogFence(server.ID, "4"), candidateFor(t, server.ID, "sample", "one"))
	require.NoError(t, err)
	reappeared, err := descriptorByName(context.Background(), repository, server.ID, "one")
	require.NoError(t, err)
	assert.Equal(t, firstOne.ID, reappeared.ID)
	assert.Equal(t, firstOne.FirstSeenAt, reappeared.FirstSeenAt)
	assert.Nil(t, reappeared.RetiredAt)
}

func TestLookupDurableDescriptorTxReturnsCurrentAndRetiredEvidenceFacts(t *testing.T) {
	repository, serverRepository, clock, store := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "requestfacts")
	_, err := repository.Commit(context.Background(), catalogFence(server.ID, "0"), candidateFor(t, server.ID, "requestfacts", "one"))
	require.NoError(t, err)

	var current DurableDescriptor
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		var lookupErr error
		current, lookupErr = repository.LookupDurableDescriptorTx(context.Background(), transaction, server.ID, "requestfacts.one")
		return lookupErr
	}))
	assert.Equal(t, contract.EvidenceCurrent, current.State)
	assert.Equal(t, "one", current.Resource.UpstreamName)
	assert.Equal(t, "requestfacts.one", current.Resource.ExternalName)
	assert.Nil(t, current.Resource.RetiredAt)

	clock.now = clock.now.Add(time.Minute)
	_, err = repository.Commit(context.Background(), catalogFence(server.ID, "1"), NormalizedCandidate{})
	require.NoError(t, err)
	var retired DurableDescriptor
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		var lookupErr error
		retired, lookupErr = repository.LookupDurableDescriptorTx(context.Background(), transaction, server.ID, "requestfacts.one")
		return lookupErr
	}))
	assert.Equal(t, contract.EvidenceRetired, retired.State)
	assert.Equal(t, current.Resource.ID, retired.Resource.ID)
	assert.Equal(t, current.Resource.Fingerprint, retired.Resource.Fingerprint)

	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		_, lookupErr := repository.LookupDurableDescriptorTx(context.Background(), transaction, server.ID, "requestfacts.missing")
		require.ErrorIs(t, lookupErr, servers.ErrNotFound)
		return nil
	}))
	_, err = repository.LookupDurableDescriptorTx(context.Background(), nil, server.ID, "requestfacts.one")
	require.ErrorIs(t, err, servers.ErrStorageUnavailable)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, updateErr := transaction.ExecContext(context.Background(), `UPDATE tool_descriptors SET fingerprint = ? WHERE tool_id = ?`, strings.Repeat("a", 64), retired.Resource.ID)
		require.NoError(t, updateErr)
		_, lookupErr := repository.LookupDurableDescriptorTx(context.Background(), transaction, server.ID, "requestfacts.one")
		require.ErrorIs(t, lookupErr, servers.ErrStorageUnavailable)
		return nil
	}))
}

func TestRepositoryDescriptorFiltersPaginationAndRevisionCursorFence(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	_, err := repository.Commit(context.Background(), catalogFence(server.ID, "0"), candidateFor(t, server.ID, "sample", "one", "two", "three"))
	require.NoError(t, err)
	page, err := repository.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredInclude, nil, 2)
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	require.NotNil(t, page.Next)
	second, err := repository.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredInclude, page.Next, 2)
	require.NoError(t, err)
	assert.Len(t, second.Items, 1)

	clock.now = clock.now.Add(time.Minute)
	_, err = repository.Commit(context.Background(), catalogFence(server.ID, "1"), candidateFor(t, server.ID, "sample", "two"))
	require.NoError(t, err)
	_, err = repository.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredInclude, page.Next, 2)
	assert.ErrorIs(t, err, servers.ErrStaleCursor)
	active, err := repository.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredExclude, nil, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"two"}, descriptorNames(active.Items))
	retired, err := repository.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredOnly, nil, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"one", "three"}, descriptorNames(retired.Items))
}

func TestRepositoryStaleAndCapacityFailurePreservePriorSnapshot(t *testing.T) {
	repository, serverRepository, _, store := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	_, err := repository.Commit(context.Background(), catalogFence(server.ID, "0"), candidateFor(t, server.ID, "sample", "one"))
	require.NoError(t, err)
	before, err := repository.Status(context.Background(), server.ID)
	require.NoError(t, err)
	stale := catalogFence(server.ID, "1")
	stale.ExpectedDesiredRevision = "99"
	_, err = repository.Commit(context.Background(), stale, candidateFor(t, server.ID, "sample", "replacement"))
	assert.ErrorIs(t, err, servers.ErrStaleRevision)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for index := int64(1); index < fixedLimit("durable_tool_identities_per_server"); index++ {
			id, idErr := admin.NewID(catalogTime, &catalogEntropy{value: uint64(index + 1000)})
			if idErr != nil {
				return idErr
			}
			name := fmt.Sprintf("reserved_%d", index)
			if _, insertErr := transaction.Exec(`INSERT INTO durable_tool_identities (id, server_id, upstream_name, external_name, first_seen_at) VALUES (?, ?, ?, ?, ?)`, id, server.ID, name, "sample."+name, catalogTime.Format(time.RFC3339Nano)); insertErr != nil {
				return insertErr
			}
		}
		return nil
	}))
	candidate := candidateFor(t, server.ID, "sample", "one", "new-a")
	_, err = repository.Commit(context.Background(), catalogFence(server.ID, "1"), candidate)
	assert.ErrorIs(t, err, servers.ErrResourceLimit)
	after, err := repository.Status(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	current, err := repository.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredExclude, nil, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"one"}, descriptorNames(current.Items))
}

func TestRepositoryRejectsEveryStaleAuthorityAndCatalogFenceWithoutMutation(t *testing.T) {
	repository, serverRepository, _, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	tests := map[string]func(*CommitFence){
		"registration":      func(fence *CommitFence) { fence.ExpectedRegistrationRevision = "1" },
		"static credential": func(fence *CommitFence) { fence.ExpectedCredentialRevisions.StaticCredential = "1" },
		"oauth client":      func(fence *CommitFence) { fence.ExpectedCredentialRevisions.OAuthClient = "1" },
		"oauth tokens":      func(fence *CommitFence) { fence.ExpectedCredentialRevisions.OAuthTokens = "1" },
		"catalog":           func(fence *CommitFence) { fence.ExpectedCatalogRevision = "1" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			fence := catalogFence(server.ID, "0")
			change(&fence)
			_, err := repository.Commit(context.Background(), fence, candidateFor(t, server.ID, "sample", "one"))
			assert.ErrorIs(t, err, servers.ErrStaleRevision)
			status, statusErr := repository.Status(context.Background(), server.ID)
			require.NoError(t, statusErr)
			assert.Equal(t, DurableStatus{State: contract.DurableCatalogEmpty}, status)
		})
	}
}

func TestRepositoryCommitMarkerUncertaintyRecoversDurableSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	armed := false
	store, err := storage.InitializeWithFaultInjection(context.Background(), ownership, catalogInstallationID, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			return errors.New("injected post-commit uncertainty")
		}
		return nil
	})
	require.NoError(t, err)
	clock := &catalogClock{now: catalogTime}
	entropy := new(catalogEntropy)
	serverRepository, err := servers.New(store, clock, entropy)
	require.NoError(t, err)
	repository, err := NewRepository(store, clock, entropy)
	require.NoError(t, err)
	server := createCatalogServer(t, serverRepository, "sample")
	armed = true
	_, err = repository.Commit(context.Background(), catalogFence(server.ID, "0"), candidateFor(t, server.ID, "sample", "one"))
	assert.ErrorIs(t, err, servers.ErrStorageUnavailable)
	assert.True(t, store.Latched())
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	_, err = storage.VerifyCurrent(context.Background(), root)
	require.NoError(t, err)
	reopenedOwnership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	reopened, err := storage.Open(context.Background(), reopenedOwnership)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = reopened.Close()
		_ = reopenedOwnership.Close()
	})
	recovered, err := NewRepository(reopened, clock, entropy)
	require.NoError(t, err)
	status, err := recovered.Status(context.Background(), server.ID)
	require.NoError(t, err)
	require.NotNil(t, status.Revision)
	assert.Equal(t, "1", *status.Revision)
	assert.Equal(t, contract.DurableCatalogCurrent, status.State)
	page, err := recovered.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredExclude, nil, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"one"}, descriptorNames(page.Items))
}

func TestRepositoryGlobalIdentityCapacityRejectsWithoutMutation(t *testing.T) {
	repository, serverRepository, _, store := newCatalogRepository(t)
	serversByID := make([]servers.Server, 0, 9)
	for index := range 9 {
		serversByID = append(serversByID, createCatalogServer(t, serverRepository, fmt.Sprintf("s%d", index)))
	}
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		global := int64(0)
		for serverIndex := range 8 {
			for toolIndex := int64(0); toolIndex < fixedLimit("durable_tool_identities_per_server"); toolIndex++ {
				id, idErr := admin.NewID(catalogTime, &catalogEntropy{value: uint64(global + 10000)})
				if idErr != nil {
					return idErr
				}
				name := fmt.Sprintf("reserved_%d", toolIndex)
				if _, insertErr := transaction.Exec(`INSERT INTO durable_tool_identities (id, server_id, upstream_name, external_name, first_seen_at) VALUES (?, ?, ?, ?, ?)`, id, serversByID[serverIndex].ID, name, fmt.Sprintf("s%d.%s", serverIndex, name), catalogTime.Format(time.RFC3339Nano)); insertErr != nil {
					return insertErr
				}
				global++
			}
		}
		return nil
	}))
	target := serversByID[8]
	before, err := repository.Status(context.Background(), target.ID)
	require.NoError(t, err)
	_, err = repository.Commit(context.Background(), catalogFence(target.ID, "0"), candidateFor(t, target.ID, "s8", "one"))
	assert.ErrorIs(t, err, servers.ErrResourceLimit)
	after, err := repository.Status(context.Background(), target.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestCatalogBackupRetainsDurableEvidenceWithoutActiveFacts(t *testing.T) {
	repository, serverRepository, _, store := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	_, err := repository.Commit(context.Background(), catalogFence(server.ID, "0"), candidateFor(t, server.ID, "sample", "one"))
	require.NoError(t, err)
	backup := filepath.Join(t.TempDir(), "catalog.db")
	require.NoError(t, store.BackupTo(context.Background(), backup))
	database, err := sql.Open("sqlite3", "file:"+backup+"?mode=ro")
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	var identities, descriptors int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM durable_tool_identities`).Scan(&identities))
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM tool_descriptors`).Scan(&descriptors))
	assert.Equal(t, 1, identities)
	assert.Equal(t, 1, descriptors)
	var activeTables int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name LIKE '%active_catalog%'`).Scan(&activeTables))
	assert.Zero(t, activeTables)
}

func TestRepositoryFinalizesDisabledAndDeletedLifecycleStates(t *testing.T) {
	for _, test := range []struct {
		name  string
		state contract.DesiredServerState
		want  contract.DurableCatalogState
	}{
		{name: "disabled", state: contract.DesiredServerDisabled, want: contract.DurableCatalogStale},
		{name: "deleted", state: contract.DesiredServerDeleted, want: contract.DurableCatalogRetired},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, serverRepository, _, _ := newCatalogRepository(t)
			server := createCatalogServer(t, serverRepository, "lifecycle-"+test.name)
			_, err := repository.Commit(context.Background(), catalogFence(server.ID, "0"), candidateFor(t, server.ID, server.Namespace, "one"))
			require.NoError(t, err)
			if test.state == contract.DesiredServerDisabled {
				enabled := false
				patched, patchErr := serverRepository.Patch(context.Background(), server.ID, server.DesiredRevision, servers.Patch{Enabled: &enabled})
				require.NoError(t, patchErr)
				server = patched.Server
			} else {
				deleted, deleteErr := serverRepository.Delete(context.Background(), server.ID, server.DesiredRevision)
				require.NoError(t, deleteErr)
				server = deleted.Server
			}
			authority, err := serverRepository.Authority(context.Background(), server.ID)
			require.NoError(t, err)
			status, err := repository.SetLifecycleState(context.Background(), CommitFence{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: authority.RegistrationRevision, ExpectedCredentialRevisions: authority.CredentialRevisions, ExpectedCatalogRevision: "1"}, test.state, test.want)
			require.NoError(t, err)
			assert.Equal(t, test.want, status.State)
		})
	}
}

func TestCatalogMigrationSeedsExistingServersAndNewServerInitialization(t *testing.T) {
	repository, serverRepository, _, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "new")
	status, err := repository.Status(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, DurableStatus{State: contract.DurableCatalogEmpty}, status)
}

func newCatalogRepository(t *testing.T) (*Repository, *servers.Repository, *catalogClock, *storage.Store) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, catalogInstallationID)
	require.NoError(t, err)
	clock := &catalogClock{now: catalogTime}
	entropy := new(catalogEntropy)
	serverRepository, err := servers.New(store, clock, entropy)
	require.NoError(t, err)
	repository, err := NewRepository(store, clock, entropy)
	require.NoError(t, err)
	t.Cleanup(func() {
		if !store.Latched() {
			_ = store.Close()
		}
		_ = ownership.Close()
	})
	return repository, serverRepository, clock, store
}

func createCatalogServer(t *testing.T, repository *servers.Repository, namespace string) servers.Server {
	t.Helper()
	digest := sha256.Sum256([]byte(namespace))
	created, err := repository.Create(context.Background(), servers.CreateRequest{Definition: servers.Definition{Namespace: namespace, DisplayName: namespace, Enabled: true, Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}}, Idempotency: &servers.IdempotencyRequest{AuthorityID: catalogInstallationID, Method: "POST", Route: "/api/v1/servers", Key: namespace, RequestHash: digest}})
	require.NoError(t, err)
	return created.Server
}

func catalogFence(serverID, catalogRevision string) CommitFence {
	return CommitFence{ServerID: serverID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedCredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}, ExpectedCatalogRevision: catalogRevision}
}

func candidateFor(t *testing.T, serverID, namespace string, names ...string) NormalizedCandidate {
	t.Helper()
	candidate := NormalizedCandidate{Tools: make([]NormalizedTool, 0, len(names)), RawCount: int64(len(names)), Pages: 1}
	for _, name := range names {
		raw := RawTool{UpstreamName: name, ExternalName: namespace + "." + name, Descriptor: []byte(`{"name":"` + name + `","inputSchema":{"type":"object"}}`)}
		normalized, err := NormalizeTool(raw, NormalizeOptions{ServerID: serverID})
		require.NoError(t, err)
		candidate.Tools = append(candidate.Tools, normalized)
	}
	return candidate
}

func descriptorByName(ctx context.Context, repository *Repository, serverID, name string) (contract.ToolDescriptor, error) {
	page, err := repository.ListDescriptors(ctx, serverID, contract.DescriptorRetiredInclude, nil, 100)
	if err != nil {
		return contract.ToolDescriptor{}, err
	}
	for _, item := range page.Items {
		if item.Resource.UpstreamName == name {
			return item.Resource, nil
		}
	}
	return contract.ToolDescriptor{}, servers.ErrNotFound
}

func descriptorNames(items []DescriptorRecord) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.Resource.UpstreamName)
	}
	return result
}

var _ io.Reader = (*catalogEntropy)(nil)
