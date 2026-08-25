package authorization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthenticateReturnsExactSafeCurrentBinding(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	creation, err := repository.IssueCredential(context.Background(), principal.ID, "1")
	require.NoError(t, err)

	binding, err := repository.Authenticate(context.Background(), creation.Bearer)
	require.NoError(t, err)
	require.NotNil(t, creation.Principal.Credential)
	assert.Equal(t, CredentialBinding{
		PrincipalID: principal.ID, PrincipalRevision: "2", Visibility: contract.VisibilityRequestable,
		CredentialID: creation.Principal.Credential.ID, CredentialRevision: "1", CredentialFingerprint: creation.Principal.Credential.Fingerprint,
	}, binding)

	encoded, err := json.Marshal(binding)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), creation.Bearer)
	assert.NotContains(t, string(encoded), hex.EncodeToString(expectedAgentVerifier(creation.Bearer)))
}

func TestAuthenticateRejectsWrongDomainMalformedAndUnknownWithoutEnumeration(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	creation, err := repository.IssueCredential(context.Background(), principal.ID, "1")
	require.NoError(t, err)
	require.NotNil(t, creation.Principal.Credential)

	adminBearer := contract.AdminBearerPrefix + strings.Repeat("a", 43)
	_, err = repository.Authenticate(context.Background(), adminBearer)
	assert.ErrorIs(t, err, ErrCredentialDomainMismatch)
	adminVerifier := sha256.Sum256(append([]byte("mcp-gateway/admin-verifier/v1\x00"), adminBearer...))
	forgedAgentBearer := contract.AgentBearerPrefix + base64.RawURLEncoding.EncodeToString(adminVerifier[:])
	for _, bearer := range []string{
		"", "foreign", contract.AgentBearerPrefix, contract.AgentBearerPrefix + "!", contract.AgentBearerPrefix + "AA",
		contract.AgentBearerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 31)),
		contract.AgentBearerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 33)),
		contract.AgentBearerPrefix + strings.Repeat("A", 43) + "=", forgedAgentBearer,
		contract.AgentBearerPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xFF}, 32)),
	} {
		_, authErr := repository.Authenticate(context.Background(), bearer)
		assert.ErrorIs(t, authErr, ErrAuthenticationRequired, bearer)
		for _, secret := range []string{bearer, principal.ID, creation.Principal.Credential.ID, creation.Principal.Credential.Fingerprint, hex.EncodeToString(expectedAgentVerifier(creation.Bearer))} {
			if secret != "" {
				assert.NotContains(t, authErr.Error(), secret)
			}
		}
	}
}

func TestAuthenticateRejectsReplacedRevokedDisabledAndAbsentAuthority(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	first, err := repository.IssueCredential(context.Background(), principal.ID, "1")
	require.NoError(t, err)
	second, err := repository.IssueCredential(context.Background(), principal.ID, "2")
	require.NoError(t, err)
	_, err = repository.Authenticate(context.Background(), first.Bearer)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	_, err = repository.Authenticate(context.Background(), second.Bearer)
	require.NoError(t, err)

	revoked, err := repository.RevokeCredential(context.Background(), principal.ID, "3")
	require.NoError(t, err)
	_, err = repository.Authenticate(context.Background(), second.Bearer)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	third, err := repository.IssueCredential(context.Background(), principal.ID, revoked.Revision)
	require.NoError(t, err)
	disabled := contract.PrincipalDisabled
	disabledPrincipal, err := repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: third.Principal.Revision, State: &disabled})
	require.NoError(t, err)
	_, err = repository.Authenticate(context.Background(), third.Bearer)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	active := contract.PrincipalActive
	_, err = repository.PatchPrincipal(context.Background(), principal.ID, PatchPrincipalRequest{ExpectedRevision: disabledPrincipal.Revision, State: &active})
	require.NoError(t, err)
	_, err = repository.Authenticate(context.Background(), third.Bearer)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
}

func TestAuthenticateScansEveryCandidateForFirstMiddleLastAndNoMatch(t *testing.T) {
	repository, _ := newRepository(t, nil)
	bearers := make([]string, 3)
	for index := range bearers {
		principal := mustCreatePrincipal(t, repository)
		creation, err := repository.IssueCredential(context.Background(), principal.ID, "1")
		require.NoError(t, err)
		bearers[index] = creation.Bearer
	}
	unknown := contract.AgentBearerPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xF0}, 32))
	for _, bearer := range append(bearers, unknown) {
		comparisons := 0
		binding, err := repository.authenticate(context.Background(), bearer, func(left, right []byte) int {
			comparisons++
			return subtle.ConstantTimeCompare(left, right)
		})
		assert.Equal(t, 3, comparisons)
		if bearer == unknown {
			assert.ErrorIs(t, err, ErrAuthenticationRequired)
			assert.Equal(t, CredentialBinding{}, binding)
		} else {
			require.NoError(t, err)
			assert.NotEmpty(t, binding.PrincipalID)
		}
	}
}

func TestAuthenticateFailsClosedOnInvalidOrOverCapacityLoadedCandidates(t *testing.T) {
	t.Run("invalid candidate after a match", func(t *testing.T) {
		repository, store := newRepository(t, nil)
		first := mustCreatePrincipal(t, repository)
		firstCreation, err := repository.IssueCredential(context.Background(), first.ID, "1")
		require.NoError(t, err)
		last := mustCreatePrincipal(t, repository)
		_, err = repository.IssueCredential(context.Background(), last.ID, "1")
		require.NoError(t, err)
		require.NoError(t, store.Mutate(context.Background(), uncheckedMutation(`UPDATE principals SET credential_fingerprint = 'invalid' WHERE id = '`+last.ID+`'`)))
		binding, err := repository.Authenticate(context.Background(), firstCreation.Bearer)
		assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
		assert.Equal(t, CredentialBinding{}, binding)
	})

	t.Run("disabled row retains a slot", func(t *testing.T) {
		repository, store := newRepository(t, nil)
		principal := mustCreatePrincipal(t, repository)
		creation, err := repository.IssueCredential(context.Background(), principal.ID, "1")
		require.NoError(t, err)
		require.NoError(t, store.Mutate(context.Background(), uncheckedMutation(`UPDATE principals SET state = 'disabled' WHERE id = '`+principal.ID+`'`)))
		_, err = repository.Authenticate(context.Background(), creation.Bearer)
		assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
	})

	t.Run("N plus one", func(t *testing.T) {
		repository, store := newRepository(t, nil)
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			for index := 0; index < 129; index++ {
				_, err := transaction.Exec(`INSERT INTO principals
					(id, display_name, state, visibility, revision, credential_revision, created_at, updated_at)
					VALUES (?, 'Agent', 'active', 'requestable', 1, 0, ?, ?)`, wideID(index+2000), timestamp(testNow), timestamp(testNow))
				if err != nil {
					return err
				}
			}
			return nil
		}))
		unknown := contract.AgentBearerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		_, err := repository.Authenticate(context.Background(), unknown)
		assert.ErrorIs(t, err, ErrAuthorizationUnavailable)
	})
}

func TestAuthenticateRejectsLatchBeforeAndAfterApparentMatch(t *testing.T) {
	t.Run("before read", func(t *testing.T) {
		armed := false
		repository, store, ownership := newFaultCredentialRepository(t, func(point storage.FaultPoint) error {
			if armed && point == storage.FaultAfterCommit {
				return errors.New("latch")
			}
			return nil
		})
		principal := mustCreatePrincipal(t, repository)
		creation, err := repository.IssueCredential(context.Background(), principal.ID, "1")
		require.NoError(t, err)
		armed = true
		assert.ErrorIs(t, store.Mutate(context.Background(), func(*sql.Tx) error { return nil }), storage.ErrStorageLatched)
		binding, err := repository.Authenticate(context.Background(), creation.Bearer)
		assert.ErrorIs(t, err, ErrStorageUnavailable)
		assert.Equal(t, CredentialBinding{}, binding)
		require.NoError(t, store.Close())
		require.NoError(t, ownership.Close())
	})

	t.Run("after match", func(t *testing.T) {
		armed := false
		repository, store, ownership := newFaultCredentialRepository(t, func(point storage.FaultPoint) error {
			if armed && point == storage.FaultAfterCommit {
				return errors.New("latch")
			}
			return nil
		})
		principal := mustCreatePrincipal(t, repository)
		creation, err := repository.IssueCredential(context.Background(), principal.ID, "1")
		require.NoError(t, err)
		armed = true
		triggered := false
		binding, err := repository.authenticate(context.Background(), creation.Bearer, func(left, right []byte) int {
			if !triggered {
				triggered = true
				assert.ErrorIs(t, store.Mutate(context.Background(), func(*sql.Tx) error { return nil }), storage.ErrStorageLatched)
			}
			return subtle.ConstantTimeCompare(left, right)
		})
		assert.True(t, triggered)
		assert.ErrorIs(t, err, ErrStorageUnavailable)
		assert.Equal(t, CredentialBinding{}, binding)
		require.NoError(t, store.Close())
		require.NoError(t, ownership.Close())
	})
}

func TestAdminResetDoesNotChangeAgentAuthentication(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	creation, err := repository.IssueCredential(context.Background(), principal.ID, "1")
	require.NoError(t, err)
	adminEntropy := make([]byte, 256)
	for index := range adminEntropy {
		adminEntropy[index] = byte(index%251 + 1)
	}
	service := admin.NewService(store, &fixedClock{now: testNow}, bytes.NewReader(adminEntropy))
	sink := &capturingSecretSink{}
	_, err = service.Initialize(context.Background(), sink)
	require.NoError(t, err)
	_, err = service.Reset(context.Background(), sink)
	require.NoError(t, err)

	binding, err := repository.Authenticate(context.Background(), creation.Bearer)
	require.NoError(t, err)
	assert.Equal(t, principal.ID, binding.PrincipalID)
	loaded, err := repository.GetPrincipal(context.Background(), principal.ID)
	require.NoError(t, err)
	assert.Equal(t, creation.Principal, loaded)
}

func TestAuthenticatePropagatesCancellation(t *testing.T) {
	repository, _ := newRepository(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bearer := contract.AgentBearerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	_, err := repository.Authenticate(ctx, bearer)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestAuthenticationSourceUsesConstantTimeBoundedScanAndRemainsUnwired(t *testing.T) {
	source, err := os.ReadFile("authentication.go")
	require.NoError(t, err)
	contents := string(source)
	assert.Contains(t, contents, "subtle.ConstantTimeCompare")
	assert.Contains(t, contents, `mustLimit("principals")`)
	assert.NotContains(t, contents, "WHERE credential_verifier =")
	assert.NotContains(t, contents, "bytes.Equal")
	assert.NotContains(t, contents, "mcpingress")

	rootSource, err := os.ReadFile(filepath.Join("..", "..", "cmd", "mcp-gateway", "root.go"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(rootSource), "mcpingress.DenyAllAuthenticator{}"))
}

type capturingSecretSink struct{ values []string }

func (sink *capturingSecretSink) Publish(value string) error {
	sink.values = append(sink.values, value)
	return nil
}
