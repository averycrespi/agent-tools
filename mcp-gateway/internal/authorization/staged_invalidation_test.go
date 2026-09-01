package authorization

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvalidateStagedCredentialsClearsManyThenNoopsAtZero(t *testing.T) {
	var first, second, absent contract.Principal
	var firstBearer, secondBearer string
	repository, store, stagedPath := newStagedAuthorizationStore(t, func(repository *Repository) {
		first = mustCreatePrincipal(t, repository)
		issued, err := repository.IssueCredential(context.Background(), first.ID, first.Revision)
		require.NoError(t, err)
		first, firstBearer = issued.Principal, issued.Bearer
		second = mustCreatePrincipal(t, repository)
		issued, err = repository.IssueCredential(context.Background(), second.ID, second.Revision)
		require.NoError(t, err)
		second, secondBearer = issued.Principal, issued.Bearer
		absent = mustCreatePrincipal(t, repository)
		_, err = repository.CreateGrant(context.Background(), CreateGrantRequest{Name: "Test grant",
			PrincipalID: first.ID, Effect: contract.GrantDeny, ServerID: id(51),
		}, allowCurrentTarget)
		require.NoError(t, err)
	})

	beforeSynthetic, err := repository.SyntheticIdentity(context.Background())
	require.NoError(t, err)
	beforeRevision, err := repository.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	beforeGrants, err := repository.ListGrants(context.Background(), GrantFilter{}, nil, 100)
	require.NoError(t, err)
	gatewayRevision := gatewayRevision(t, store)

	require.NoError(t, InvalidateStagedCredentials(context.Background(), store, targetInspector{}))
	for _, expected := range []contract.Principal{first, second} {
		actual, loadErr := repository.GetPrincipal(context.Background(), expected.ID)
		require.NoError(t, loadErr)
		expected.Revision = incrementRevision(t, expected.Revision)
		expected.CredentialRevision = incrementRevision(t, expected.CredentialRevision)
		expected.Credential = nil
		assert.Equal(t, expected, actual)
	}
	actualAbsent, err := repository.GetPrincipal(context.Background(), absent.ID)
	require.NoError(t, err)
	assert.Equal(t, absent, actualAbsent)
	assertStagedPolicyUnchanged(t, repository, beforeSynthetic, beforeRevision, beforeGrants, gatewayRevision, store)
	require.NoError(t, repository.ValidateStartup(context.Background(), targetInspector{}))

	firstPass := principalRows(t, store)
	require.NoError(t, InvalidateStagedCredentials(context.Background(), store, targetInspector{}))
	assert.Equal(t, firstPass, principalRows(t, store), "zero-credential surgery must be an exact no-op")
	assertStagedPolicyUnchanged(t, repository, beforeSynthetic, beforeRevision, beforeGrants, gatewayRevision, store)

	require.NoError(t, store.Checkpoint(context.Background()))
	for _, path := range []string{stagedPath, stagedPath + "-wal", stagedPath + "-shm", stagedPath + ".mutation", stagedPath + ".mutation.tmp", stagedPath + ".mutation.cleared"} {
		contents, readErr := os.ReadFile(path)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		require.NoError(t, readErr)
		assert.False(t, bytes.Contains(contents, []byte(firstBearer)), filepath.Base(path))
		assert.False(t, bytes.Contains(contents, []byte(secondBearer)), filepath.Base(path))
	}
}

func TestInvalidateStagedCredentialsRollsBackAllRowsOnMidStatementFailure(t *testing.T) {
	var failID string
	repository, store, _ := newStagedAuthorizationStore(t, func(repository *Repository) {
		first := mustCreatePrincipal(t, repository)
		_, err := repository.IssueCredential(context.Background(), first.ID, first.Revision)
		require.NoError(t, err)
		second := mustCreatePrincipal(t, repository)
		_, err = repository.IssueCredential(context.Background(), second.ID, second.Revision)
		require.NoError(t, err)
		failID = second.ID
	})
	before := principalRows(t, store)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`CREATE TRIGGER fail_staged_credential_invalidation
			BEFORE UPDATE OF credential_id ON principals
			WHEN OLD.id = '` + failID + `'
			BEGIN SELECT RAISE(ABORT, 'injected staged invalidation failure'); END`)
		return err
	}))

	err := InvalidateStagedCredentials(context.Background(), store, targetInspector{})
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.False(t, store.Latched())
	assert.Equal(t, before, principalRows(t, store))
	require.NoError(t, repository.ValidateStartup(context.Background(), targetInspector{}))
}

func TestInvalidateStagedCredentialsRejectsPartialSlotBeforeWriting(t *testing.T) {
	var corruptID string
	_, store, _ := newStagedAuthorizationStore(t, func(repository *Repository) {
		corrupt := mustCreatePrincipal(t, repository)
		_, err := repository.IssueCredential(context.Background(), corrupt.ID, corrupt.Revision)
		require.NoError(t, err)
		corruptID = corrupt.ID
		other := mustCreatePrincipal(t, repository)
		_, err = repository.IssueCredential(context.Background(), other.ID, other.Revision)
		require.NoError(t, err)
	})
	require.NoError(t, store.Mutate(context.Background(), uncheckedMutation(`UPDATE principals SET credential_fingerprint = NULL WHERE id = '`+corruptID+`'`)))
	before := principalRows(t, store)

	err := InvalidateStagedCredentials(context.Background(), store, targetInspector{})
	assert.ErrorIs(t, err, ErrInvalidState)
	assert.False(t, store.Latched())
	assert.Equal(t, before, principalRows(t, store))
}

func TestInvalidateStagedCredentialsRequiresStoreAndInspector(t *testing.T) {
	assert.ErrorIs(t, InvalidateStagedCredentials(context.Background(), nil, targetInspector{}), ErrInvalidInput)
	repository, store, _ := newStagedAuthorizationStore(t, nil)
	_ = repository
	assert.ErrorIs(t, InvalidateStagedCredentials(context.Background(), store, nil), ErrInvalidInput)
}

type stagedPrincipalRow struct {
	ID                    string
	Revision              int64
	CredentialRevision    int64
	CredentialID          sql.NullString
	CredentialVerifier    []byte
	CredentialFingerprint sql.NullString
	CredentialCreatedAt   sql.NullString
}

func principalRows(t *testing.T, store *storage.Store) []stagedPrincipalRow {
	t.Helper()
	var result []stagedPrincipalRow
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		rows, err := transaction.Query(`SELECT id, revision, credential_revision, credential_id,
			credential_verifier, credential_fingerprint, credential_created_at FROM principals ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var row stagedPrincipalRow
			if err := rows.Scan(&row.ID, &row.Revision, &row.CredentialRevision, &row.CredentialID,
				&row.CredentialVerifier, &row.CredentialFingerprint, &row.CredentialCreatedAt); err != nil {
				return err
			}
			row.CredentialVerifier = append([]byte(nil), row.CredentialVerifier...)
			result = append(result, row)
		}
		return rows.Err()
	}))
	return result
}

func assertStagedPolicyUnchanged(
	t *testing.T,
	repository *Repository,
	synthetic SyntheticIdentity,
	revision string,
	grants GrantPage,
	gatewayRevisionBefore int64,
	store *storage.Store,
) {
	t.Helper()
	actualSynthetic, err := repository.SyntheticIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, synthetic, actualSynthetic)
	actualRevision, err := repository.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, revision, actualRevision)
	actualGrants, err := repository.ListGrants(context.Background(), GrantFilter{}, nil, 100)
	require.NoError(t, err)
	assert.Equal(t, grants, actualGrants)
	assert.Equal(t, gatewayRevisionBefore, gatewayRevision(t, store))
}

func gatewayRevision(t *testing.T, store *storage.Store) int64 {
	t.Helper()
	var revision int64
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT revision FROM gateway_meta WHERE singleton = 1`).Scan(&revision)
	}))
	return revision
}

func incrementRevision(t *testing.T, revision string) string {
	t.Helper()
	value, err := strconv.ParseInt(revision, 10, 64)
	require.NoError(t, err)
	return strconv.FormatInt(value+1, 10)
}

func newStagedAuthorizationStore(
	t *testing.T,
	populate func(*Repository),
) (*Repository, *storage.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	live, err := storage.Initialize(context.Background(), ownership, testInstallationID)
	require.NoError(t, err)
	entropy := make([]byte, 4096)
	for index := range entropy {
		entropy[index] = byte(index%251 + 1)
	}
	liveRepository, err := New(live, &fixedClock{now: testNow}, bytes.NewReader(entropy))
	require.NoError(t, err)
	if populate != nil {
		populate(liveRepository)
	}
	require.NoError(t, live.Checkpoint(context.Background()))
	layout := ownership.Layout()
	require.NoError(t, live.Close())
	contents, err := os.ReadFile(layout.Database)
	require.NoError(t, err)
	stagedPath := filepath.Join(layout.Root, "restore-stage.sqlite")
	require.NoError(t, os.WriteFile(stagedPath, contents, 0o600))
	replacement, err := storage.OpenReplacement(context.Background(), ownership, stagedPath)
	require.NoError(t, err)
	repository, err := New(replacement, &fixedClock{now: testNow}, bytes.NewReader(bytes.Repeat([]byte{0xA5}, 4096)))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = replacement.Close()
		_ = ownership.Close()
	})
	return repository, replacement, stagedPath
}
