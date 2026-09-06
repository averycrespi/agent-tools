package authorization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentCredentialAuditRollbackAndAttribution(t *testing.T) {
	for _, action := range []string{"issue", "revoke", "invalidate"} {
		t.Run(action, func(t *testing.T) {
			repository, store := newRepository(t, nil)
			principal := mustCreatePrincipal(t, repository)
			issued, err := repository.IssueCredential(t.Context(), principal.ID, principal.Revision)
			require.NoError(t, err)
			before := issued.Principal
			verifier := credentialVerifier(t, store, principal.ID)
			credential := contract.AuditCredential{ID: id(70), Fingerprint: "0123456789abcdef"}
			ctx := audit.WithOperator(t.Context(), credential, credential.ID)
			mutate := func() error {
				switch action {
				case "issue":
					result, err := repository.IssueCredential(ctx, principal.ID, before.Revision)
					if err != nil {
						assert.Empty(t, result.Bearer)
					}
					return err
				case "revoke":
					_, err := repository.RevokeCredential(ctx, principal.ID, before.Revision)
					return err
				default:
					disabled := contract.PrincipalDisabled
					_, err := repository.PatchPrincipal(ctx, principal.ID, PatchPrincipalRequest{ExpectedRevision: before.Revision, State: &disabled})
					return err
				}
			}
			require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER refuse_credential_audit BEFORE INSERT ON control_audit_events WHEN NEW.category = 'agent_credential' AND json_extract(NEW.event, '$.phase') = 'outcome' BEGIN SELECT RAISE(ABORT, 'refused audit'); END`)
				return err
			}))
			require.ErrorContains(t, mutate(), "refused audit")
			after, err := repository.GetPrincipal(t.Context(), principal.ID)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Equal(t, verifier, credentialVerifier(t, store, principal.ID))
			reader, err := audit.NewRepository(store)
			require.NoError(t, err)
			query := audit.Query{Limit: 100, Filters: contract.AuditFilters{CorrelationID: credential.ID}}
			page, err := reader.List(t.Context(), query)
			require.NoError(t, err)
			assert.Empty(t, page.Items)
			require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), `DROP TRIGGER refuse_credential_audit`)
				return err
			}))
			require.NoError(t, mutate())
			page, err = reader.List(t.Context(), query)
			require.NoError(t, err)
			expected := 4
			if action == "revoke" {
				expected = 2
			}
			require.Len(t, page.Items, expected)
			for _, event := range page.Items {
				if event.Action == "invalidate" {
					assert.Equal(t, before.Credential.ID, event.Target.ID)
					assert.Equal(t, contract.AuditSystem, event.Actor.Type)
					assert.Equal(t, &credential, event.Initiator)
				} else {
					assert.Equal(t, contract.AuditOperator, event.Actor.Type)
					assert.Equal(t, &credential, event.Actor.Credential)
				}
			}
			encoded, err := json.Marshal(page)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), issued.Bearer)
		})
	}
}

func TestIssueReplaceAndRevokeCurrentCredential(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)

	first, err := repository.IssueCredential(context.Background(), principal.ID, "1")
	require.NoError(t, err)
	assert.Equal(t, "2", first.Principal.Revision)
	assert.Equal(t, "1", first.Principal.CredentialRevision)
	require.NotNil(t, first.Principal.Credential)
	assert.Equal(t, "1", first.Principal.Credential.Revision)
	assert.Equal(t, timestamp(testNow), first.Principal.Credential.CreatedAt)
	assert.True(t, strings.HasPrefix(first.Bearer, contract.AgentBearerPrefix))
	encoded := strings.TrimPrefix(first.Bearer, contract.AgentBearerPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	assert.Len(t, decoded, 32)
	assert.Equal(t, expectedAgentFingerprint(first.Bearer), first.Principal.Credential.Fingerprint)
	assert.Equal(t, expectedAgentVerifier(first.Bearer), credentialVerifier(t, store, principal.ID))
	assert.Equal(t, "1", authorizationRevision(t, repository), "credential issue is not a policy mutation")

	second, err := repository.IssueCredential(context.Background(), principal.ID, "2")
	require.NoError(t, err)
	assert.Equal(t, "3", second.Principal.Revision)
	assert.Equal(t, "2", second.Principal.CredentialRevision)
	require.NotNil(t, second.Principal.Credential)
	assert.Equal(t, "2", second.Principal.Credential.Revision)
	assert.NotEqual(t, first.Bearer, second.Bearer)
	assert.NotEqual(t, first.Principal.Credential.ID, second.Principal.Credential.ID)
	assert.Equal(t, expectedAgentVerifier(second.Bearer), credentialVerifier(t, store, principal.ID))
	assert.Equal(t, "1", authorizationRevision(t, repository))

	revoked, err := repository.RevokeCredential(context.Background(), principal.ID, "3")
	require.NoError(t, err)
	assert.Equal(t, "4", revoked.Revision)
	assert.Equal(t, "3", revoked.CredentialRevision)
	assert.Nil(t, revoked.Credential)
	assert.Equal(t, "1", authorizationRevision(t, repository))

	_, err = repository.RevokeCredential(context.Background(), principal.ID, "4")
	assert.ErrorIs(t, err, ErrConflict)
	_, err = repository.IssueCredential(context.Background(), principal.ID, "3")
	assert.ErrorIs(t, err, ErrStaleRevision)
	loaded, err := repository.GetPrincipal(context.Background(), principal.ID)
	require.NoError(t, err)
	assert.Equal(t, revoked, loaded)
}

func TestCredentialLifecycleThroughDisableAndReenable(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	issued, err := repository.IssueCredential(context.Background(), principal.ID, "1")
	require.NoError(t, err)

	disabled := contract.PrincipalDisabled
	updated, err := repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: issued.Principal.Revision, State: &disabled})
	require.NoError(t, err)
	assert.Equal(t, "3", updated.Revision)
	assert.Equal(t, "2", updated.CredentialRevision)
	assert.Nil(t, updated.Credential)
	assert.Equal(t, "2", authorizationRevision(t, repository))

	_, err = repository.IssueCredential(context.Background(), principal.ID, updated.Revision)
	assert.ErrorIs(t, err, ErrConflict)
	active := contract.PrincipalActive
	updated, err = repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: updated.Revision, State: &active})
	require.NoError(t, err)
	assert.Equal(t, "4", updated.Revision)
	assert.Equal(t, "2", updated.CredentialRevision)
	assert.Nil(t, updated.Credential)
	assert.Equal(t, "3", authorizationRevision(t, repository))

	reissued, err := repository.IssueCredential(context.Background(), principal.ID, updated.Revision)
	require.NoError(t, err)
	assert.Equal(t, "5", reissued.Principal.Revision)
	assert.Equal(t, "3", reissued.Principal.CredentialRevision)
	assert.NotNil(t, reissued.Principal.Credential)
}

func TestCredentialMutationsRejectInvalidMissingStaleAndExhaustedState(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	for _, revision := range []string{"", "0", "01", "-1", "foreign"} {
		_, err := repository.IssueCredential(context.Background(), principal.ID, revision)
		assert.ErrorIs(t, err, ErrInvalidInput)
		_, err = repository.RevokeCredential(context.Background(), principal.ID, revision)
		assert.ErrorIs(t, err, ErrInvalidInput)
	}
	_, err := repository.IssueCredential(context.Background(), id(99), "1")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = repository.RevokeCredential(context.Background(), id(99), "1")
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`UPDATE principals SET revision = 9223372036854775807 WHERE id = ?`, principal.ID)
		return err
	}))
	_, err = repository.IssueCredential(context.Background(), principal.ID, "9223372036854775807")
	assert.ErrorIs(t, err, ErrConflict)
}

func TestCredentialEntropyIdentityAndKnownRollbackFailuresPreserveAuthority(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)

	shortBearer, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(make([]byte, 31)))
	require.NoError(t, err)
	_, err = shortBearer.IssueCredential(context.Background(), principal.ID, "1")
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	shortID, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(make([]byte, 32+9)))
	require.NoError(t, err)
	_, err = shortID.IssueCredential(context.Background(), principal.ID, "1")
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	reservedID, err := New(store, &fixedClock{now: unixEpochClock{}.Now()}, bytes.NewReader(make([]byte, 42)))
	require.NoError(t, err)
	_, err = reservedID.IssueCredential(context.Background(), principal.ID, "1")
	assert.ErrorIs(t, err, ErrIdentityUnavailable)

	other := mustCreatePrincipal(t, repository)
	idEntropy := bytes.Repeat([]byte{0xA5}, 10)
	identityRepository, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(idEntropy))
	require.NoError(t, err)
	collidingID, err := identityRepository.newID(testNow)
	require.NoError(t, err)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`UPDATE principals SET revision = 2, credential_revision = 1,
			credential_id = ?, credential_verifier = ?, credential_fingerprint = '0123456789abcdef', credential_created_at = ?
			WHERE id = ?`, collidingID, make([]byte, 32), timestamp(testNow), other.ID)
		return err
	}))
	collisionEntropy := append(bytes.Repeat([]byte{0x5A}, 32), idEntropy...)
	collisionRepository, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(collisionEntropy))
	require.NoError(t, err)
	_, err = collisionRepository.IssueCredential(context.Background(), principal.ID, "1")
	assert.ErrorIs(t, err, ErrIdentityUnavailable)

	issued, err := repository.IssueCredential(context.Background(), principal.ID, "1")
	require.NoError(t, err)
	issuedSecret, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(issued.Bearer, contract.AgentBearerPrefix))
	require.NoError(t, err)
	repeatedBearerEntropy := make([]byte, 0, 42)
	repeatedBearerEntropy = append(repeatedBearerEntropy, issuedSecret...)
	repeatedBearerEntropy = append(repeatedBearerEntropy, bytes.Repeat([]byte{0xB6}, 10)...)
	repeatedBearer, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(repeatedBearerEntropy))
	require.NoError(t, err)
	_, err = repeatedBearer.IssueCredential(context.Background(), principal.ID, issued.Principal.Revision)
	assert.ErrorIs(t, err, ErrIdentityUnavailable)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`CREATE TRIGGER fail_credential_replace BEFORE UPDATE OF credential_id ON principals BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		return err
	}))
	_, err = repository.IssueCredential(context.Background(), principal.ID, issued.Principal.Revision)
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.False(t, store.Latched())
	loaded, err := repository.GetPrincipal(context.Background(), principal.ID)
	require.NoError(t, err)
	assert.Equal(t, issued.Principal, loaded)
	assert.Equal(t, expectedAgentVerifier(issued.Bearer), credentialVerifier(t, store, principal.ID))
}

func TestCredentialIssueFaultsNeverReturnBearerAndRecoverCommittedCandidate(t *testing.T) {
	for _, point := range []storage.FaultPoint{storage.FaultArmCreate, storage.FaultAfterCommit} {
		t.Run(string(point), func(t *testing.T) {
			ctx := context.Background()
			armed := false
			repository, store, ownership := newFaultCredentialRepository(t, func(got storage.FaultPoint) error {
				if armed && got == point {
					return errors.New("injected credential publication fault")
				}
				return nil
			})
			principal := mustCreatePrincipal(t, repository)
			armed = true
			creation, err := repository.IssueCredential(ctx, principal.ID, "1")
			assert.ErrorIs(t, err, ErrStorageUnavailable)
			assert.Empty(t, creation.Bearer)
			assert.True(t, store.Latched())
			root := ownership.Layout().Root
			require.NoError(t, store.Close())
			require.NoError(t, ownership.Close())

			_, err = storage.VerifyCurrent(ctx, root)
			require.NoError(t, err)
			principalRevision, credentialRevision, credentialID := readRecoveredCredentialState(t, root, principal.ID)
			if point == storage.FaultArmCreate {
				assert.Equal(t, int64(1), principalRevision)
				assert.Equal(t, int64(0), credentialRevision)
			} else {
				assert.Equal(t, int64(3), principalRevision)
				assert.Equal(t, int64(2), credentialRevision)
			}
			assert.Nil(t, credentialID)
		})
	}
}

func TestUncertainReplacementNeverRestoresPriorCredential(t *testing.T) {
	ctx := context.Background()
	armed := false
	repository, store, ownership := newFaultCredentialRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			return errors.New("replacement acknowledgement lost")
		}
		return nil
	})
	principal := mustCreatePrincipal(t, repository)
	first, err := repository.IssueCredential(ctx, principal.ID, "1")
	require.NoError(t, err)
	armed = true
	creation, err := repository.IssueCredential(ctx, principal.ID, "2")
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.Empty(t, creation.Bearer)
	root := ownership.Layout().Root
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	_, err = storage.VerifyCurrent(ctx, root)
	require.NoError(t, err)
	principalRevision, credentialRevision, credentialID := readRecoveredCredentialState(t, root, principal.ID)
	assert.Equal(t, int64(4), principalRevision)
	assert.Equal(t, int64(3), credentialRevision)
	assert.Nil(t, credentialID)
	assert.NotEmpty(t, first.Bearer)
}

func TestUncertainRevokeRemainsCommittedWithoutCandidateRevision(t *testing.T) {
	ctx := context.Background()
	armed := false
	repository, store, ownership := newFaultCredentialRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			return errors.New("revoke acknowledgement lost")
		}
		return nil
	})
	principal := mustCreatePrincipal(t, repository)
	_, err := repository.IssueCredential(ctx, principal.ID, "1")
	require.NoError(t, err)
	armed = true
	_, err = repository.RevokeCredential(ctx, principal.ID, "2")
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	root := ownership.Layout().Root
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	_, err = storage.VerifyCurrent(ctx, root)
	require.NoError(t, err)
	principalRevision, credentialRevision, credentialID := readRecoveredCredentialState(t, root, principal.ID)
	assert.Equal(t, int64(3), principalRevision)
	assert.Equal(t, int64(2), credentialRevision)
	assert.Nil(t, credentialID)
}

func TestAgentBearerCanaryAppearsOnlyInCreationResult(t *testing.T) {
	ctx := context.Background()
	repository, store, ownership := newFaultCredentialRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	creation, err := repository.IssueCredential(ctx, principal.ID, "1")
	require.NoError(t, err)
	bearer := []byte(creation.Bearer)

	principalJSON, err := json.Marshal(creation.Principal)
	require.NoError(t, err)
	assert.NotContains(t, string(principalJSON), creation.Bearer)
	loaded, err := repository.GetPrincipal(ctx, principal.ID)
	require.NoError(t, err)
	loadedJSON, err := json.Marshal(loaded)
	require.NoError(t, err)
	assert.NotContains(t, string(loadedJSON), creation.Bearer)
	_, staleErr := repository.IssueCredential(ctx, principal.ID, "1")
	assert.NotContains(t, staleErr.Error(), creation.Bearer)

	for _, path := range []string{ownership.Layout().Database, ownership.Layout().Database + "-wal", ownership.Layout().Database + "-shm", ownership.Layout().MutationMarker, ownership.Layout().MutationMarker + ".tmp", ownership.Layout().MutationMarker + ".cleared"} {
		contents, readErr := os.ReadFile(path)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		require.NoError(t, readErr)
		assert.False(t, bytes.Contains(contents, bearer), filepath.Base(path))
	}
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
}

func expectedAgentVerifier(bearer string) []byte {
	digest := sha256.Sum256(append([]byte("mcp-gateway/agent-verifier/v1\x00"), bearer...))
	return digest[:]
}

func expectedAgentFingerprint(bearer string) string {
	verifier := expectedAgentVerifier(bearer)
	digest := sha256.Sum256(append([]byte("mcp-gateway/agent-fingerprint/v1\x00"), verifier...))
	return hex.EncodeToString(digest[:8])
}

func credentialVerifier(t *testing.T, store *storage.Store, principalID string) []byte {
	t.Helper()
	var verifier []byte
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT credential_verifier FROM principals WHERE id = ?`, principalID).Scan(&verifier)
	}))
	return verifier
}

type unixEpochClock struct{}

func (unixEpochClock) Now() time.Time { return time.UnixMilli(0).UTC() }

func newFaultCredentialRepository(t *testing.T, fault func(storage.FaultPoint) error) (*Repository, *storage.Store, *gatewaypaths.Ownership) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	var store *storage.Store
	if fault == nil {
		store, err = storage.Initialize(context.Background(), ownership, testInstallationID)
	} else {
		store, err = storage.InitializeWithFaultInjection(context.Background(), ownership, testInstallationID, fault)
	}
	require.NoError(t, err)
	entropy := make([]byte, 2048)
	for index := range entropy {
		entropy[index] = byte(index%251 + 1)
	}
	repository, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(entropy))
	require.NoError(t, err)
	return repository, store, ownership
}

func readRecoveredCredentialState(t *testing.T, root, principalID string) (int64, int64, *string) {
	t.Helper()
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()
	store, err := storage.Open(context.Background(), ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var principalRevision, credentialRevision, authorizationRevision int64
	var credentialID sql.NullString
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		if err := transaction.QueryRow(`SELECT revision, credential_revision, credential_id FROM principals WHERE id = ?`, principalID).Scan(&principalRevision, &credentialRevision, &credentialID); err != nil {
			return err
		}
		return transaction.QueryRow(`SELECT revision FROM authorization_meta WHERE singleton = 1`).Scan(&authorizationRevision)
	}))
	assert.Equal(t, int64(1), authorizationRevision, "credential publication, revocation, and recovery do not change policy revision")
	if !credentialID.Valid {
		return principalRevision, credentialRevision, nil
	}
	return principalRevision, credentialRevision, &credentialID.String
}
