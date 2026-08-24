package servers

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthFlowCreateSupersedeAwaitCancelAndExactExpiry(t *testing.T) {
	clock := &mutableClock{now: testTime}
	repository, _, _ := newRepositoryWithClock(t, clock, new(sequenceReader))
	server := mustCreateOAuthServer(t, repository, "flow-server", contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})

	first, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowPreparing, first.Flow.State)
	assert.Equal(t, "0", first.Flow.RegistrationRevision)
	assert.Equal(t, formatTime(testTime.Add(contract.OAuthFlowLifetime)), first.Flow.ExpiresAt)
	assert.Empty(t, first.SupersededIDs)

	second, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	assert.Equal(t, []string{first.Flow.ID}, second.SupersededIDs)
	storedFirst, err := repository.GetAuthFlow(context.Background(), server.ID, first.Flow.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowSuperseded, storedFirst.State)
	assert.Equal(t, contract.ReasonSuperseded, *storedFirst.Reason)

	authority := testRegistrationAuthority(contract.RegistrationDynamic, contract.TokenEndpointAuthNone)
	_, err = repository.PublishPublicRegistration(context.Background(), RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0"}, authority)
	require.NoError(t, err)
	awaiting, err := repository.MarkAuthFlowAwaiting(context.Background(), second.Flow.ID, "1", "1")
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowAwaitingCallback, awaiting.State)
	assert.Equal(t, "1", awaiting.RegistrationRevision)

	cancelled, err := repository.CancelAuthFlow(context.Background(), server.ID, second.Flow.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowCancelled, cancelled.State)
	assert.Equal(t, contract.ReasonCancelled, *cancelled.Reason)
	_, err = repository.CancelAuthFlow(context.Background(), server.ID, second.Flow.ID)
	require.NoError(t, err)

	expiring, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	clock.now = testTime.Add(contract.OAuthFlowLifetime)
	expiredIDs, err := repository.ExpireAuthFlows(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{expiring.Flow.ID}, expiredIDs)
	expired, err := repository.GetAuthFlow(context.Background(), server.ID, expiring.Flow.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowExpired, expired.State)
	assert.Equal(t, contract.ReasonOAuthExpired, *expired.Reason)
}

func TestAuthFlowStaticEligibilityRequiresConfidentialClientAuthority(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	issuer := "https://issuer.example"
	public := mustCreateOAuthServer(t, repository, "flow-static-public", contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, Issuer: &issuer, ClientID: "public-client", TokenEndpointAuthMethod: contract.TokenEndpointAuthNone})
	_, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: public.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	confidential := mustCreateOAuthServer(t, repository, "flow-static-confidential", contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, Issuer: &issuer, ClientID: "confidential-client", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretBasic})
	_, err = repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: confidential.ID, ExpectedDesiredRevision: "1"})
	assert.ErrorIs(t, err, ErrInvalidOperation)
}

func TestAuthFlowFencesRegistrationPublicationAfterSupersession(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	server := mustCreateOAuthServer(t, repository, "flow-registration-fence", contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	first, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	second, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	authority := testRegistrationAuthority(contract.RegistrationDynamic, contract.TokenEndpointAuthNone)
	_, err = repository.PublishPublicRegistration(context.Background(), RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0", ExpectedAuthFlowID: first.Flow.ID}, authority)
	assert.ErrorIs(t, err, ErrStaleRevision)
	_, err = repository.PublishPublicRegistration(context.Background(), RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0", ExpectedAuthFlowID: second.Flow.ID}, authority)
	require.NoError(t, err)
}

func TestBeginAuthFlowExchangeRechecksEveryCapturedAuthority(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	server := mustCreateOAuthServer(t, repository, "flow-exchange-fence", contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	created, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	_, err = repository.PublishPublicRegistration(context.Background(), RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0", ExpectedAuthFlowID: created.Flow.ID}, testRegistrationAuthority(contract.RegistrationDynamic, contract.TokenEndpointAuthNone))
	require.NoError(t, err)
	_, err = repository.MarkAuthFlowAwaiting(context.Background(), created.Flow.ID, "1", "1")
	require.NoError(t, err)
	fence := OAuthTokenFence{ServerID: server.ID, FlowID: created.Flow.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "1", ExpectedOAuthClientRevision: "0", ExpectedOAuthTokensRevision: "0"}
	exchanging, err := repository.BeginAuthFlowExchange(context.Background(), fence)
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowExchanging, exchanging.State)
	_, err = repository.BeginAuthFlowExchange(context.Background(), fence)
	assert.ErrorIs(t, err, ErrStaleRevision)
}

func TestAuthFlowExchangingConflictsAndGlobalAdmissionIsImmediate(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	server := mustCreateOAuthServer(t, repository, "flow-conflict", contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	created, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	_, err = repository.PublishPublicRegistration(context.Background(), RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0"}, testRegistrationAuthority(contract.RegistrationDynamic, contract.TokenEndpointAuthNone))
	require.NoError(t, err)
	_, err = repository.MarkAuthFlowAwaiting(context.Background(), created.Flow.ID, "1", "1")
	require.NoError(t, err)
	_, err = repository.TransitionAuthFlow(context.Background(), created.Flow.ID, contract.AuthFlowExchanging, nil)
	require.NoError(t, err)
	_, err = repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	assert.ErrorIs(t, err, ErrOAuthFlowActive)
	_, err = repository.CancelAuthFlow(context.Background(), server.ID, created.Flow.ID)
	assert.ErrorIs(t, err, ErrOAuthFlowActive)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for index := int64(1); index < mustLimit("oauth_flows"); index++ {
			identity := flowTestID(index + 200)
			if _, err := transaction.Exec(`INSERT INTO server_identities (id, namespace, created_at) VALUES (?, ?, ?)`, identity, "flow-limit-"+time.Unix(index, 0).UTC().Format("150405"), formatTime(testTime)); err != nil {
				return err
			}
			transport := `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"oauth","registration":{"mode":"dynamic","issuer":null},"trusted_origins":[],"request_offline_access":false}}`
			if _, err := transaction.Exec(`INSERT INTO servers (id, display_name, desired_state, desired_revision, transport_json, created_at, updated_at, deleted_at) VALUES (?, ?, 'disabled', 1, ?, ?, ?, NULL)`, identity, identity, transport, formatTime(testTime), formatTime(testTime)); err != nil {
				return err
			}
			if _, err := transaction.Exec(`INSERT INTO server_oauth_registrations (server_id, revision) VALUES (?, 0)`, identity); err != nil {
				return err
			}
			for _, kind := range []contract.ServerCredentialKind{contract.ServerCredentialStatic, contract.ServerCredentialOAuthClient, contract.ServerCredentialOAuthTokens} {
				if _, err := transaction.Exec(`INSERT INTO server_credentials (server_id, kind, revision, handle) VALUES (?, ?, 0, NULL)`, identity, kind); err != nil {
					return err
				}
			}
			if _, err := transaction.Exec(`INSERT INTO server_auth_flow_watermarks (server_id, pruning_generation) VALUES (?, 0)`, identity); err != nil {
				return err
			}
			if _, err := transaction.Exec(`INSERT INTO server_auth_flows (id, server_id, flow_state, target_desired_revision, registration_revision, created_at, expires_at) VALUES (?, ?, 'preparing', 1, 0, ?, ?)`, flowTestID(index+500), identity, formatTime(testTime), formatTime(testTime.Add(contract.OAuthFlowLifetime))); err != nil {
				return err
			}
		}
		return nil
	}))
	other := mustCreateOAuthServer(t, repository, "flow-over-limit", contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	_, err = repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: other.ID, ExpectedDesiredRevision: "1"})
	assert.ErrorIs(t, err, ErrResourceLimit)
	status, err := repository.AuthFlowStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, mustLimit("oauth_flows"), status.InUse)
	assert.True(t, status.Saturated)
}

func TestBehavioralServerMutationSupersedesAuthFlow(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	server := mustCreateOAuthServer(t, repository, "flow-behavior", contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	created, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	enabled := true
	_, err = repository.Patch(context.Background(), server.ID, "1", Patch{Enabled: &enabled})
	require.NoError(t, err)
	flow, err := repository.GetAuthFlow(context.Background(), server.ID, created.Flow.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowSuperseded, flow.State)
}

func TestAuthFlowListSnapshotsAndTerminalRetention(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	server := mustCreateOAuthServer(t, repository, "flow-history", contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	empty, err := repository.ListAuthFlows(context.Background(), server.ID, nil, 10)
	require.NoError(t, err)
	assert.Empty(t, empty.Items)

	for index := int64(0); index < mustLimit("terminal_auth_flows"); index++ {
		created, createErr := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
		require.NoError(t, createErr)
		reason := contract.ReasonOAuthRejected
		_, transitionErr := repository.TransitionAuthFlow(context.Background(), created.Flow.ID, contract.AuthFlowFailed, &reason)
		require.NoError(t, transitionErr)
	}
	page, err := repository.ListAuthFlows(context.Background(), server.ID, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, page.Next)
	created, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	reason := contract.ReasonOAuthRejected
	_, err = repository.TransitionAuthFlow(context.Background(), created.Flow.ID, contract.AuthFlowFailed, &reason)
	require.NoError(t, err)
	_, err = repository.ListAuthFlows(context.Background(), server.ID, page.Next, 1)
	assert.ErrorIs(t, err, ErrStaleCursor)
	retained, err := repository.ListAuthFlows(context.Background(), server.ID, nil, int(mustLimit("terminal_auth_flows")))
	require.NoError(t, err)
	assert.Len(t, retained.Items, int(mustLimit("terminal_auth_flows")))
}

func flowTestID(value int64) string {
	return fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G%04d", value)
}

func TestAuthFlowRestartInterruptsAndDurableSchemaHasNoTransientColumns(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	server := mustCreateOAuthServer(t, repository, "flow-restart", contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})
	created, err := repository.CreateAuthFlow(context.Background(), AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	require.NoError(t, repository.InterruptAuthFlows(context.Background()))
	interrupted, err := repository.GetAuthFlow(context.Background(), server.ID, created.Flow.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.AuthFlowInterrupted, interrupted.State)
	assert.Equal(t, contract.ReasonInterrupted, *interrupted.Reason)

	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		rows, err := transaction.Query(`PRAGMA table_info(server_auth_flows)`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var sequence, notNull, primaryKey int
			var name, kind string
			var defaultValue any
			require.NoError(t, rows.Scan(&sequence, &name, &kind, &notNull, &defaultValue, &primaryKey))
			assert.NotContains(t, []string{"state", "verifier", "authorization_url", "authorization_endpoint", "token_endpoint", "code", "access_token", "refresh_token"}, name)
		}
		return rows.Err()
	}))
}
