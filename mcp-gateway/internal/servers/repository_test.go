package servers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var testTime = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func stringPointer(value string) *string { return &value }

func TestDecodeTransportRejectsMalformedClosedUnionsAndSemantics(t *testing.T) {
	valid := []struct {
		name string
		raw  string
		want any
	}{
		{name: "stdio", raw: `{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}`, want: contract.StdioTransport{}},
		{name: "HTTP none", raw: `{"kind":"streamable_http","url":"http://127.0.0.1:9000/mcp","protocol_mode":"modern","authentication":{"mode":"none"}}`, want: contract.StreamableHTTPTransport{}},
		{name: "HTTP bearer", raw: `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"legacy","authentication":{"mode":"bearer"}}`, want: contract.StreamableHTTPTransport{}},
		{name: "HTTP static OAuth", raw: `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"auto","authentication":{"mode":"oauth","registration":{"mode":"static","client_id":"client","token_endpoint_auth_method":"none"},"trusted_origins":[],"request_offline_access":false}}`, want: contract.StreamableHTTPTransport{}},
		{name: "HTTP dynamic OAuth", raw: `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"oauth","registration":{"mode":"dynamic"},"trusted_origins":[],"request_offline_access":false}}`, want: contract.StreamableHTTPTransport{}},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := DecodeTransport([]byte(test.raw))
			require.NoError(t, err)
			assert.IsType(t, test.want, decoded)
		})
	}

	invalid := []struct {
		name string
		raw  string
	}{
		{name: "unknown transport", raw: `{"kind":"websocket"}`},
		{name: "unknown stdio member", raw: `{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{},"extra":true}`},
		{name: "relative stdio path", raw: `{"kind":"stdio","executable":"server","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}`},
		{name: "unknown authentication", raw: `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"basic"}}`},
		{name: "authentication union mismatch", raw: `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"none","registration":{"mode":"dynamic"}}}`},
		{name: "unknown registration", raw: `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"oauth","registration":{"mode":"manual"},"trusted_origins":[],"request_offline_access":false}}`},
		{name: "OAuth over HTTP", raw: `{"kind":"streamable_http","url":"http://127.0.0.1:9000/mcp","protocol_mode":"modern","authentication":{"mode":"oauth","registration":{"mode":"dynamic"},"trusted_origins":[],"request_offline_access":false}}`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeTransport([]byte(test.raw))
			assert.ErrorIs(t, err, ErrInvalidInput)
		})
	}
}

type mutableClock struct{ now time.Time }

func (clock *mutableClock) Now() time.Time { return clock.now }

type sequenceReader struct{ value uint64 }

func (reader *sequenceReader) Read(buffer []byte) (int, error) {
	reader.value++
	for index := range buffer {
		buffer[index] = byte(reader.value >> (8 * (index % 8)))
	}
	return len(buffer), nil
}

func TestRepositoryUsesS1ULIDsAndPermanentNamespaceTombstones(t *testing.T) {
	repository, _, _ := newRepository(t, bytes.NewReader(make([]byte, 64)))

	id, err := repository.NewID()
	require.NoError(t, err)
	assert.Equal(t, "01M0MNF4G00000000000000000", id)
	_, err = keyring.NewNamespace(testInstallationID, id, keyring.RecordStaticCredential)
	require.NoError(t, err)

	created, err := repository.Create(context.Background(), CreateRequest{
		ID:          id,
		Definition:  Definition{Namespace: "alpha", DisplayName: "Alpha", Enabled: false, Transport: testStdioTransport()},
		Idempotency: idempotency("create-alpha", "create-alpha", ""),
	})
	require.NoError(t, err)
	assert.Equal(t, "1", created.Server.DesiredRevision)

	_, err = repository.Create(context.Background(), CreateRequest{
		ID:          id,
		Definition:  Definition{Namespace: "beta", DisplayName: "Beta", Enabled: false, Transport: testStdioTransport()},
		Idempotency: idempotency("duplicate-id", "duplicate-id", ""),
	})
	assert.ErrorIs(t, err, ErrIdentityUnavailable)

	deleted, err := repository.Delete(context.Background(), id, "1")
	require.NoError(t, err)
	assert.Equal(t, contract.DesiredServerDeleted, deleted.Server.DesiredState)
	assert.Equal(t, "2", deleted.Server.DesiredRevision)
	assert.Nil(t, deleted.Server.Transport)

	otherID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	_, err = repository.Create(context.Background(), CreateRequest{
		ID:          otherID,
		Definition:  Definition{Namespace: "alpha", DisplayName: "Replacement", Enabled: false, Transport: testStdioTransport()},
		Idempotency: idempotency("reuse-tombstone", "reuse-tombstone", ""),
	})
	assert.ErrorIs(t, err, ErrNamespaceUnavailable)

	replay, err := repository.Delete(context.Background(), id, "2")
	require.NoError(t, err)
	assert.True(t, replay.Replayed)
	assert.Equal(t, "2", replay.Server.DesiredRevision)
}

func TestRegistryStatusCountsPermanentIdentitiesAndNondeletedServers(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	identities, activeServers, err := repository.RegistryStatus(context.Background())
	require.NoError(t, err)
	assert.Zero(t, identities.InUse)
	assert.Zero(t, activeServers.InUse)

	created, err := repository.Create(context.Background(), CreateRequest{Definition: Definition{Namespace: "occupancy", DisplayName: "Occupancy", Enabled: false, Transport: testStdioTransport()}, Idempotency: idempotency("occupancy-create", "occupancy-create", "")})
	require.NoError(t, err)
	identities, activeServers, err = repository.RegistryStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), identities.InUse)
	assert.Equal(t, int64(1), activeServers.InUse)

	_, err = repository.Delete(context.Background(), created.Server.ID, created.Server.DesiredRevision)
	require.NoError(t, err)
	identities, activeServers, err = repository.RegistryStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), identities.InUse)
	assert.Zero(t, activeServers.InUse)
}

func TestDesiredRevisionsCanonicalizePatchAndDelete(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	created, err := repository.Create(context.Background(), CreateRequest{
		Definition: Definition{Namespace: "canonical", DisplayName: "Before", Enabled: false, Transport: contract.StdioTransport{
			Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp",
			Environment: map[string]string{"Z": "2", "A": "1"}, SecretEnvironment: map[string]string{},
		}},
		Idempotency: idempotency("canonical-create", "canonical-create", ""),
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"arguments":[],"environment":{"A":"1","Z":"2"},"executable":"/bin/true","kind":"stdio","secret_environment":{},"working_directory":"/tmp"}`, string(created.Server.Transport))
	assert.Equal(t, `{"arguments":[],"environment":{"A":"1","Z":"2"},"executable":"/bin/true","kind":"stdio","secret_environment":{},"working_directory":"/tmp"}`, string(created.Server.Transport))

	name := "After"
	enabled := true
	patched, err := repository.Patch(context.Background(), created.Server.ID, "1", Patch{DisplayName: &name, Enabled: &enabled})
	require.NoError(t, err)
	assert.Equal(t, "2", patched.DesiredRevision)
	assert.Equal(t, contract.DesiredServerEnabled, patched.DesiredState)

	_, err = repository.Patch(context.Background(), created.Server.ID, "1", Patch{DisplayName: &name})
	assert.ErrorIs(t, err, ErrStaleRevision)

	deleted, err := repository.Delete(context.Background(), created.Server.ID, "2")
	require.NoError(t, err)
	assert.Equal(t, "3", deleted.Server.DesiredRevision)
	assert.NotNil(t, deleted.Server.DeletedAt)
}

func TestDesiredMutationsCreateOnlyBehavioralOperations(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	enabledCreate := CreateRequest{Definition: Definition{Namespace: "enabled", DisplayName: "Enabled", Enabled: true, Transport: testStdioTransport()}, Idempotency: idempotency("create-enabled", "create-enabled", "")}
	created, err := repository.Create(context.Background(), enabledCreate)
	require.NoError(t, err)
	require.NotNil(t, created.Operation)
	assert.Equal(t, contract.OperationActivate, created.Operation.Kind)
	replayed, err := repository.Create(context.Background(), enabledCreate)
	require.NoError(t, err)
	require.NotNil(t, replayed.Operation)
	assert.Equal(t, created.Operation.ID, replayed.Operation.ID)

	disabled := mustCreateServer(t, repository, "behavior", false)
	name := "Display only"
	display, err := repository.Patch(context.Background(), disabled.ID, "1", Patch{DisplayName: &name})
	require.NoError(t, err)
	assert.Nil(t, display.Operation)

	enabled := true
	behavioral, err := repository.Patch(context.Background(), disabled.ID, "2", Patch{Enabled: &enabled})
	require.NoError(t, err)
	require.NotNil(t, behavioral.Operation)
	assert.Equal(t, contract.OperationActivate, behavioral.Operation.Kind)
	assert.Equal(t, "3", behavioral.Operation.TargetDesiredRevision)

	deleted, err := repository.Delete(context.Background(), disabled.ID, "3")
	require.NoError(t, err)
	require.NotNil(t, deleted.Operation)
	assert.Equal(t, contract.OperationDelete, deleted.Operation.Kind)
	replay, err := repository.Delete(context.Background(), disabled.ID, "4")
	require.NoError(t, err)
	assert.True(t, replay.Replayed)
	require.NotNil(t, replay.Operation)
	assert.Equal(t, deleted.Operation.ID, replay.Operation.ID)
}

func TestDesiredTransportValidationRejectsSecretAndRemoteBoundaryViolations(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	tests := []struct {
		name      string
		transport contract.Transport
	}{
		{name: "relative executable", transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "server", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}},
		{name: "ambiguous secret environment", transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{"TOKEN": "not-secret"}, SecretEnvironment: map[string]string{"TOKEN": "token"}}},
		{name: "remote plain HTTP", transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "http://example.com/mcp", ProtocolMode: contract.ProtocolAuto, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}}},
		{name: "bearer loopback plain HTTP", transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "http://127.0.0.1:9000/mcp", ProtocolMode: contract.ProtocolAuto, Authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}}},
		{name: "URL query", transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://example.com/mcp?secret=value", ProtocolMode: contract.ProtocolAuto, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}}},
		{name: "noncanonical trusted origin", transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://example.com/mcp", ProtocolMode: contract.ProtocolAuto, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}, TrustedOrigins: []string{"https://EXAMPLE.com"}}}},
		{name: "trusted origin path", transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://example.com/mcp", ProtocolMode: contract.ProtocolAuto, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}, TrustedOrigins: []string{"https://trusted.example/"}}}},
		{name: "query-bearing issuer", transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://example.com/mcp", ProtocolMode: contract.ProtocolAuto, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic, Issuer: stringPointer("https://issuer.example?tenant=one")}}}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := "invalid-" + strconv.Itoa(index)
			_, err := repository.Create(context.Background(), CreateRequest{Definition: Definition{Namespace: "invalid", DisplayName: "Invalid", Transport: test.transport}, Idempotency: idempotency(key, key, "")})
			assert.ErrorIs(t, err, ErrInvalidInput)
		})
	}

	_, err := repository.Create(context.Background(), CreateRequest{Definition: Definition{Namespace: "loopback", DisplayName: "Loopback", Transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "http://127.0.0.1:9000/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}}}, Idempotency: idempotency("valid-loopback", "valid-loopback", "")})
	require.NoError(t, err)
}

func TestZeroRegistrationFenceAndIndependentCredentialMetadata(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	created := mustCreateServer(t, repository, "authority", false)

	authority, err := repository.Authority(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "0", authority.RegistrationRevision)
	assert.Equal(t, contract.CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}, authority.CredentialRevisions)
	assert.Nil(t, authority.StaticCredentialHandle)
	assert.Nil(t, authority.OAuthClientHandle)
	assert.Nil(t, authority.OAuthTokensHandle)

	handle, err := keyring.NewHandle(bytes.NewReader(bytes.Repeat([]byte{0x33}, 32)))
	require.NoError(t, err)
	revision := applyCredentialCallback(t, repository, store, CredentialFence{
		ServerID: created.ID, Kind: contract.ServerCredentialStatic,
		ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0",
	}, &handle)
	assert.Equal(t, "1", revision)

	authority, err = repository.Authority(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", authority.CredentialRevisions.StaticCredential)
	assert.Equal(t, "0", authority.CredentialRevisions.OAuthClient)
	assert.Equal(t, "0", authority.CredentialRevisions.OAuthTokens)
	assert.Equal(t, "0", authority.RegistrationRevision)

	revision = applyCredentialCallback(t, repository, store, CredentialFence{
		ServerID: created.ID, Kind: contract.ServerCredentialStatic,
		ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "1",
	}, nil)
	assert.Equal(t, "2", revision)
	stale, err := repository.CredentialAuthorityCallback(CredentialFence{
		ServerID: created.ID, Kind: contract.ServerCredentialStatic,
		ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "1",
	})
	require.NoError(t, err)
	err = store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, callbackErr := stale(context.Background(), transaction, keyring.AuthorityUpdate{
			Owner: created.ID, Kind: keyring.RecordStaticCredential,
		})
		return callbackErr
	})
	assert.ErrorIs(t, err, ErrStaleRevision)
}

func TestCredentialAuthorityCallbackUsesExistingTransactionAndIndependentFences(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	created := mustCreateServer(t, repository, "publication", false)
	staticHandle, err := keyring.NewHandle(bytes.NewReader(bytes.Repeat([]byte{0x41}, 32)))
	require.NoError(t, err)
	callback, err := repository.CredentialAuthorityCallback(CredentialFence{
		ServerID: created.ID, Kind: contract.ServerCredentialStatic,
		ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0",
	})
	require.NoError(t, err)

	var revision string
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		var callbackErr error
		revision, callbackErr = callback(context.Background(), transaction, keyring.AuthorityUpdate{
			Owner: created.ID, Kind: keyring.RecordStaticCredential, Handle: &staticHandle,
		})
		return callbackErr
	}))
	assert.Equal(t, "1", revision)

	oauthHandle, err := keyring.NewHandle(bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)))
	require.NoError(t, err)
	assert.Equal(t, "1", applyCredentialCallback(t, repository, store, CredentialFence{
		ServerID: created.ID, Kind: contract.ServerCredentialOAuthClient,
		ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0",
	}, &oauthHandle))
	nextStaticHandle, err := keyring.NewHandle(bytes.NewReader(bytes.Repeat([]byte{0x43}, 32)))
	require.NoError(t, err)
	callback, err = repository.CredentialAuthorityCallback(CredentialFence{
		ServerID: created.ID, Kind: contract.ServerCredentialStatic,
		ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "1",
	})
	require.NoError(t, err)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, callbackErr := callback(context.Background(), transaction, keyring.AuthorityUpdate{
			Owner: created.ID, Kind: keyring.RecordStaticCredential, Handle: &nextStaticHandle,
		})
		return callbackErr
	}))
	authority, err := repository.Authority(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.CredentialRevisions{StaticCredential: "2", OAuthClient: "1", OAuthTokens: "0"}, authority.CredentialRevisions)

	stale, err := repository.CredentialAuthorityCallback(CredentialFence{
		ServerID: created.ID, Kind: contract.ServerCredentialStatic,
		ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "1",
	})
	require.NoError(t, err)
	err = store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, callbackErr := stale(context.Background(), transaction, keyring.AuthorityUpdate{
			Owner: created.ID, Kind: keyring.RecordStaticCredential, Handle: &staticHandle,
		})
		return callbackErr
	})
	assert.ErrorIs(t, err, ErrStaleRevision)

	name := "Publication changed"
	_, err = repository.Patch(context.Background(), created.ID, "1", Patch{DisplayName: &name})
	require.NoError(t, err)
	desiredStale, err := repository.CredentialAuthorityCallback(CredentialFence{
		ServerID: created.ID, Kind: contract.ServerCredentialStatic,
		ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "2",
	})
	require.NoError(t, err)
	err = store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, callbackErr := desiredStale(context.Background(), transaction, keyring.AuthorityUpdate{
			Owner: created.ID, Kind: keyring.RecordStaticCredential, Handle: &staticHandle,
		})
		return callbackErr
	})
	assert.ErrorIs(t, err, ErrStaleRevision)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, updateErr := transaction.ExecContext(context.Background(), `
			UPDATE server_oauth_registrations SET revision = 1, mode = 'static',
				issuer = 'https://issuer.example', client_id = 'client',
				callback_url = 'http://127.0.0.1:8210/oauth/callback',
				resource_url = 'https://resource.example/mcp',
				token_endpoint_auth_method = 'none', created_at = ?, client_secret_expires_at = NULL
			WHERE server_id = ?`, formatTime(testTime), created.ID)
		return updateErr
	}))
	registrationStale, err := repository.CredentialAuthorityCallback(CredentialFence{
		ServerID: created.ID, Kind: contract.ServerCredentialStatic,
		ExpectedDesiredRevision: "2", ExpectedCredentialRevision: "2", ExpectedRegistrationRevision: "0",
	})
	require.NoError(t, err)
	err = store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, callbackErr := registrationStale(context.Background(), transaction, keyring.AuthorityUpdate{
			Owner: created.ID, Kind: keyring.RecordStaticCredential, Handle: &staticHandle,
		})
		return callbackErr
	})
	assert.ErrorIs(t, err, ErrStaleRevision)
}

func TestExplicitOperationAdmissionReplayAttachmentAndSupersession(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	created, err := repository.Create(context.Background(), CreateRequest{
		Definition:  Definition{Namespace: "triggers", DisplayName: "Triggers", Enabled: true, Transport: testStdioTransport()},
		Idempotency: idempotency("create-triggers", "create-triggers", ""),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Operation)
	_, err = repository.TransitionOperation(context.Background(), created.Operation.ID, contract.OperationRunning, nil)
	require.NoError(t, err)
	_, err = repository.TransitionOperation(context.Background(), created.Operation.ID, contract.OperationSucceeded, nil)
	require.NoError(t, err)

	inactive := &OperationTriggerState{RuntimeState: contract.RuntimeInactive, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent}
	reloadRequest := OperationRequest{ServerID: created.Server.ID, Kind: contract.OperationReload, ExpectedDesiredRevision: "1", TriggerState: inactive, Idempotency: idempotency("reload", "reload", contract.ServerETag(created.Server.ID, "1"))}
	reload, err := repository.CreateOperation(context.Background(), reloadRequest)
	require.NoError(t, err)
	replayed, err := repository.CreateOperation(context.Background(), reloadRequest)
	require.NoError(t, err)
	assert.True(t, replayed.Replayed)
	assert.Equal(t, reload.Operation.ID, replayed.Operation.ID)

	conflict := reloadRequest
	conflict.Idempotency = idempotency("reload-conflict", "reload", contract.ServerETag(created.Server.ID, "1"))
	_, err = repository.CreateOperation(context.Background(), conflict)
	assert.ErrorIs(t, err, ErrOperationConflict)

	_, err = repository.TransitionOperation(context.Background(), reload.Operation.ID, contract.OperationRunning, nil)
	require.NoError(t, err)
	_, err = repository.TransitionOperation(context.Background(), reload.Operation.ID, contract.OperationSucceeded, nil)
	require.NoError(t, err)

	active := &OperationTriggerState{RuntimeState: contract.RuntimeActive, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogCurrent}
	refreshRequest := OperationRequest{ServerID: created.Server.ID, Kind: contract.OperationRefreshCatalog, ExpectedDesiredRevision: "1", TriggerState: active, Idempotency: idempotency("refresh", "refresh", contract.ServerETag(created.Server.ID, "1"))}
	refresh, err := repository.CreateOperation(context.Background(), refreshRequest)
	require.NoError(t, err)
	attachedRequest := refreshRequest
	attachedRequest.Idempotency = idempotency("refresh-attached", "refresh", contract.ServerETag(created.Server.ID, "1"))
	attached, err := repository.CreateOperation(context.Background(), attachedRequest)
	require.NoError(t, err)
	assert.False(t, attached.Replayed)
	assert.Equal(t, refresh.Operation.ID, attached.Operation.ID)

	disabled := false
	patched, err := repository.Patch(context.Background(), created.Server.ID, "1", Patch{Enabled: &disabled})
	require.NoError(t, err)
	require.NotNil(t, patched.Operation)
	assert.Equal(t, contract.OperationDisable, patched.Operation.Kind)
	superseded, err := repository.GetOperation(context.Background(), refresh.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.OperationSuperseded, superseded.State)
	assert.Equal(t, contract.ReasonSuperseded, *superseded.Reason)

	_, err = repository.TransitionOperation(context.Background(), patched.Operation.ID, contract.OperationRunning, nil)
	require.NoError(t, err)
	_, err = repository.TransitionOperation(context.Background(), patched.Operation.ID, contract.OperationSucceeded, nil)
	require.NoError(t, err)
	invalidRetry := OperationRequest{ServerID: created.Server.ID, Kind: contract.OperationRetry, ExpectedDesiredRevision: "2", TriggerState: inactive, Idempotency: idempotency("invalid-retry", "retry", contract.ServerETag(created.Server.ID, "2"))}
	_, err = repository.CreateOperation(context.Background(), invalidRetry)
	assert.ErrorIs(t, err, ErrInvalidOperation)
}

func TestExplicitOperationClosedAdmissibility(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	server := mustCreateServer(t, repository, "admission", false)
	states := []struct {
		name  string
		kind  contract.ServerOperationKind
		state OperationTriggerState
		err   error
	}{
		{name: "reload disabled", kind: contract.OperationReload, state: OperationTriggerState{RuntimeState: contract.RuntimeInactive, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent}, err: ErrInvalidOperation},
		{name: "retry inactive", kind: contract.OperationRetry, state: OperationTriggerState{RuntimeState: contract.RuntimeInactive, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent}, err: ErrInvalidOperation},
		{name: "refresh inactive", kind: contract.OperationRefreshCatalog, state: OperationTriggerState{RuntimeState: contract.RuntimeInactive, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent}, err: ErrInvalidOperation},
		{name: "disconnect absent", kind: contract.OperationDisconnectCredentials, state: OperationTriggerState{RuntimeState: contract.RuntimeInactive, CredentialState: contract.ServerCredentialAbsent, CatalogState: contract.ActiveCatalogAbsent}, err: ErrInvalidOperation},
	}
	for index, test := range states {
		t.Run(test.name, func(t *testing.T) {
			key := "admission-" + strconv.Itoa(index)
			_, err := repository.CreateOperation(context.Background(), OperationRequest{ServerID: server.ID, Kind: test.kind, ExpectedDesiredRevision: "1", TriggerState: &test.state, Idempotency: idempotency(key, key, contract.ServerETag(server.ID, "1"))})
			assert.ErrorIs(t, err, test.err)
		})
	}
}

func TestOperationTransitionsInterruptionPruningAndSnapshotStaleness(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	server := mustCreateServer(t, repository, "operations", false)

	first, err := repository.CreateOperation(context.Background(), OperationRequest{ServerID: server.ID, Kind: contract.OperationReload})
	require.NoError(t, err)
	assert.Equal(t, contract.OperationScheduled, first.Operation.State)
	running, err := repository.TransitionOperation(context.Background(), first.Operation.ID, contract.OperationRunning, nil)
	require.NoError(t, err)
	assert.NotNil(t, running.StartedAt)
	succeeded, err := repository.TransitionOperation(context.Background(), first.Operation.ID, contract.OperationSucceeded, nil)
	require.NoError(t, err)
	assert.NotNil(t, succeeded.FinishedAt)
	_, err = repository.TransitionOperation(context.Background(), first.Operation.ID, contract.OperationFailed, nil)
	assert.ErrorIs(t, err, ErrInvalidTransition)

	scheduled, err := repository.CreateOperation(context.Background(), OperationRequest{ServerID: server.ID, Kind: contract.OperationRetry})
	require.NoError(t, err)
	runningAgain, err := repository.CreateOperation(context.Background(), OperationRequest{ServerID: server.ID, Kind: contract.OperationRefreshCatalog})
	require.NoError(t, err)
	_, err = repository.TransitionOperation(context.Background(), runningAgain.Operation.ID, contract.OperationRunning, nil)
	require.NoError(t, err)
	require.NoError(t, repository.InterruptNonterminal(context.Background()))
	for _, id := range []string{scheduled.Operation.ID, runningAgain.Operation.ID} {
		operation, getErr := repository.GetOperation(context.Background(), id)
		require.NoError(t, getErr)
		assert.Equal(t, contract.OperationInterrupted, operation.State)
		assert.Equal(t, contract.ReasonInterrupted, *operation.Reason)
	}

	page, err := repository.ListOperations(context.Background(), server.ID, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, page.Next)
	staleCursor := *page.Next

	for index := 0; index < 65; index++ {
		created, createErr := repository.CreateOperation(context.Background(), OperationRequest{ServerID: server.ID, Kind: contract.OperationReload})
		require.NoError(t, createErr)
		_, transitionErr := repository.TransitionOperation(context.Background(), created.Operation.ID, contract.OperationCancelled, ptrReason(contract.ReasonCancelled))
		require.NoError(t, transitionErr)
	}
	status, err := repository.OperationStatus(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(64), status.InUse)
	assert.True(t, status.Saturated)
	_, err = repository.ListOperations(context.Background(), server.ID, &staleCursor, 1)
	assert.ErrorIs(t, err, ErrStaleCursor)
}

func TestOperationPruningKeepsOldNonterminalRowsVisibleAndStalesEarlierContinuation(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	server := mustCreateServer(t, repository, "operation-floor", false)
	oldScheduled, err := repository.CreateOperation(context.Background(), OperationRequest{
		ServerID: server.ID,
		Kind:     contract.OperationRetry,
	})
	require.NoError(t, err)

	firstTerminal, err := repository.CreateOperation(context.Background(), OperationRequest{
		ServerID: server.ID,
		Kind:     contract.OperationReload,
	})
	require.NoError(t, err)
	_, err = repository.TransitionOperation(context.Background(), firstTerminal.Operation.ID, contract.OperationCancelled, nil)
	require.NoError(t, err)
	beforePrune, err := repository.ListOperations(context.Background(), server.ID, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, beforePrune.Next)

	for range 64 {
		created, createErr := repository.CreateOperation(context.Background(), OperationRequest{
			ServerID: server.ID,
			Kind:     contract.OperationReload,
		})
		require.NoError(t, createErr)
		_, transitionErr := repository.TransitionOperation(context.Background(), created.Operation.ID, contract.OperationCancelled, nil)
		require.NoError(t, transitionErr)
	}

	_, err = repository.ListOperations(context.Background(), server.ID, beforePrune.Next, 100)
	assert.ErrorIs(t, err, ErrStaleCursor)
	fresh, err := repository.ListOperations(context.Background(), server.ID, nil, 100)
	require.NoError(t, err)
	require.Nil(t, fresh.Next)
	require.Len(t, fresh.Items, 65)
	assert.Equal(t, oldScheduled.Operation.ID, fresh.Items[0].ID)
	assert.Equal(t, contract.OperationScheduled, fresh.Items[0].State)

	postPrunePage, err := repository.ListOperations(context.Background(), server.ID, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, postPrunePage.Next)
	_, err = repository.TransitionOperation(context.Background(), oldScheduled.Operation.ID, contract.OperationCancelled, nil)
	require.NoError(t, err)
	_, err = repository.ListOperations(context.Background(), server.ID, postPrunePage.Next, 100)
	assert.ErrorIs(t, err, ErrStaleCursor)
}

func TestIdempotentTerminalOperationReplaySurvivesHistoryPruningUntilExpiry(t *testing.T) {
	clock := &mutableClock{now: testTime}
	repository, _, _ := newRepositoryWithClock(t, clock, new(sequenceReader))
	server := mustCreateServer(t, repository, "operation-replay", false)
	requests := make([]OperationRequest, 65)
	results := make([]Operation, 65)
	for index := range requests {
		requests[index] = OperationRequest{
			ServerID:    server.ID,
			Kind:        contract.OperationReload,
			Idempotency: idempotency("terminal-"+strconv.Itoa(index), "terminal-"+strconv.Itoa(index), ""),
		}
		created, err := repository.CreateOperation(context.Background(), requests[index])
		require.NoError(t, err)
		results[index], err = repository.TransitionOperation(context.Background(), created.Operation.ID, contract.OperationCancelled, nil)
		require.NoError(t, err)
	}

	status, err := repository.OperationStatus(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(64), status.InUse)
	_, err = repository.GetOperation(context.Background(), results[0].ID)
	assert.ErrorIs(t, err, ErrNotFound)

	replayed, err := repository.CreateOperation(context.Background(), requests[0])
	require.NoError(t, err)
	assert.True(t, replayed.Replayed)
	assert.Equal(t, results[0], replayed.Operation)

	clock.now = clock.now.Add(contract.IdempotencyRetention)
	reused, err := repository.CreateOperation(context.Background(), requests[0])
	require.NoError(t, err)
	assert.False(t, reused.Replayed)
	assert.NotEqual(t, results[0].ID, reused.Operation.ID)
}

func TestCreateReplayReturnsOriginalServerAfterPatchAndTombstone(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	for _, mutation := range []struct {
		name   string
		mutate func(*testing.T, *Repository, Server)
	}{
		{
			name: "patch",
			mutate: func(t *testing.T, repository *Repository, server Server) {
				name := "Changed after creation"
				_, err := repository.Patch(context.Background(), server.ID, "1", Patch{DisplayName: &name})
				require.NoError(t, err)
			},
		},
		{
			name: "tombstone",
			mutate: func(t *testing.T, repository *Repository, server Server) {
				_, err := repository.Delete(context.Background(), server.ID, "1")
				require.NoError(t, err)
			},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			request := CreateRequest{
				Definition: Definition{
					Namespace:   "create-replay-" + mutation.name,
					DisplayName: "Original",
					Enabled:     false,
					Transport:   testStdioTransport(),
				},
				Idempotency: idempotency("create-replay-"+mutation.name, "create-replay-"+mutation.name, ""),
			}
			created, err := repository.Create(context.Background(), request)
			require.NoError(t, err)
			mutation.mutate(t, repository, created.Server)

			replayed, err := repository.Create(context.Background(), request)
			require.NoError(t, err)
			assert.True(t, replayed.Replayed)
			assert.Equal(t, created.Server, replayed.Server)
			assert.Equal(t, "1", replayed.Result.DesiredRevision)
			assert.Equal(t, contract.ServerETag(created.Server.ID, "1"), contract.ServerETag(replayed.Server.ID, replayed.Server.DesiredRevision))
		})
	}
}

func TestIdempotencyLookupPrecedesPreconditionAndHandlesConflictExpiryCapacity(t *testing.T) {
	clock := &mutableClock{now: testTime}
	repository, _, _ := newRepositoryWithClock(t, clock, new(sequenceReader))
	server := mustCreateServer(t, repository, "idempotency", false)

	request := OperationRequest{
		ServerID:                server.ID,
		Kind:                    contract.OperationReload,
		ExpectedDesiredRevision: "1",
		Idempotency:             idempotency("operation-key", "reload-input", contract.ServerETag(server.ID, "1")),
	}
	first, err := repository.CreateOperation(context.Background(), request)
	require.NoError(t, err)
	assert.False(t, first.Replayed)

	name := "Changed"
	_, err = repository.Patch(context.Background(), server.ID, "1", Patch{DisplayName: &name})
	require.NoError(t, err)

	replayed, err := repository.CreateOperation(context.Background(), request)
	require.NoError(t, err)
	assert.True(t, replayed.Replayed)
	assert.Equal(t, first.Result, replayed.Result)

	conflict := request
	conflict.Idempotency = idempotency("operation-key", "different-input", contract.ServerETag(server.ID, "1"))
	_, err = repository.CreateOperation(context.Background(), conflict)
	assert.ErrorIs(t, err, ErrIdempotencyConflict)

	stale := request
	stale.Idempotency = idempotency("new-key", "reload-input", contract.ServerETag(server.ID, "1"))
	_, err = repository.CreateOperation(context.Background(), stale)
	assert.ErrorIs(t, err, ErrStaleRevision)

	clock.now = clock.now.Add(contract.IdempotencyRetention)
	expiredReuse := request
	expiredReuse.ExpectedDesiredRevision = "2"
	expiredReuse.Idempotency = idempotency("operation-key", "new-after-expiry", contract.ServerETag(server.ID, "2"))
	created, err := repository.CreateOperation(context.Background(), expiredReuse)
	require.NoError(t, err)
	assert.False(t, created.Replayed)

	seedIdempotencyCapacity(t, repository, server.ID, clock.now)
	before := operationCount(t, repository, server.ID)
	over := expiredReuse
	over.Idempotency = idempotency("over-capacity", "over-capacity", contract.ServerETag(server.ID, "2"))
	_, err = repository.CreateOperation(context.Background(), over)
	assert.ErrorIs(t, err, ErrResourceLimit)
	assert.Equal(t, before, operationCount(t, repository, server.ID))
}

func TestServerCapacityAndStorageLatchMapping(t *testing.T) {
	repository, store, ownership := newRepository(t, new(sequenceReader))
	for index := 0; index < 32; index++ {
		mustCreateServer(t, repository, "enabled-"+strconv.Itoa(index), true)
	}
	_, err := repository.Create(context.Background(), CreateRequest{
		Definition:  Definition{Namespace: "enabled-over", DisplayName: "Over", Enabled: true, Transport: testStdioTransport()},
		Idempotency: idempotency("enabled-over", "enabled-over", ""),
	})
	assert.ErrorIs(t, err, ErrResourceLimit)

	for index := 32; index < 64; index++ {
		mustCreateServer(t, repository, "disabled-"+strconv.Itoa(index), false)
	}
	_, err = repository.Create(context.Background(), CreateRequest{
		Definition:  Definition{Namespace: "server-over", DisplayName: "Over", Enabled: false, Transport: testStdioTransport()},
		Idempotency: idempotency("server-over", "server-over", ""),
	})
	assert.ErrorIs(t, err, ErrResourceLimit)

	require.NoError(t, store.Close())
	marker := ownership.Layout().MutationMarker
	require.NoError(t, os.WriteFile(marker, []byte(`{"installation_id":"`+testInstallationID+`","state":"armed"}`+"\n"), 0o600))
	latched, err := storage.Open(context.Background(), ownership)
	require.NoError(t, err)
	t.Cleanup(func() { _ = latched.Close() })
	latchedRepository, err := New(latched, &mutableClock{now: testTime}, new(sequenceReader))
	require.NoError(t, err)
	_, err = latchedRepository.Create(context.Background(), CreateRequest{
		Definition:  Definition{Namespace: "latched", DisplayName: "Latched", Enabled: false, Transport: testStdioTransport()},
		Idempotency: idempotency("latched", "latched", ""),
	})
	assert.ErrorIs(t, err, ErrStorageUnavailable)
}

func TestIdentityCapacityRejectsWithoutPartialMutation(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	limit, ok := contract.FixedLimitByName("server_identities")
	require.True(t, ok)
	require.NoError(t, repository.store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for sequence := int64(1); sequence <= limit.Maximum; sequence++ {
			id := idForSequence(sequence)
			if _, err := transaction.Exec(`INSERT INTO server_identities (id, namespace, created_at) VALUES (?, ?, ?)`, id, "tombstone-"+strconv.FormatInt(sequence, 10), formatTime(testTime)); err != nil {
				return err
			}
			if _, err := transaction.Exec(`INSERT INTO servers (id, display_name, desired_state, desired_revision, transport_json, created_at, updated_at, deleted_at) VALUES (?, ?, 'deleted', 1, NULL, ?, ?, ?)`, id, "Deleted", formatTime(testTime), formatTime(testTime), formatTime(testTime)); err != nil {
				return err
			}
		}
		return nil
	}))

	_, err := repository.Create(context.Background(), CreateRequest{
		Definition:  Definition{Namespace: "identity-over", DisplayName: "Over", Enabled: false, Transport: testStdioTransport()},
		Idempotency: idempotency("identity-over", "identity-over", ""),
	})
	assert.ErrorIs(t, err, ErrResourceLimit)
	var count int64
	require.NoError(t, repository.store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT count(*) FROM server_identities`).Scan(&count)
	}))
	assert.Equal(t, limit.Maximum, count)
}

func applyCredentialCallback(
	t *testing.T,
	repository *Repository,
	store *storage.Store,
	fence CredentialFence,
	handle *keyring.Handle,
) string {
	t.Helper()
	callback, err := repository.CredentialAuthorityCallback(fence)
	require.NoError(t, err)
	var revision string
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		var callbackErr error
		revision, callbackErr = callback(context.Background(), transaction, keyring.AuthorityUpdate{
			Owner: fence.ServerID, Kind: recordKindForCredential(fence.Kind), Handle: handle,
		})
		return callbackErr
	}))
	return revision
}

func newRepository(t *testing.T, entropy io.Reader) (*Repository, *storage.Store, *gatewaypaths.Ownership) {
	t.Helper()
	return newRepositoryWithClock(t, &mutableClock{now: testTime}, entropy)
}

func newRepositoryWithClock(t *testing.T, clock Clock, entropy io.Reader) (*Repository, *storage.Store, *gatewaypaths.Ownership) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, testInstallationID)
	require.NoError(t, err)
	repository, err := New(store, clock, entropy)
	require.NoError(t, err)
	t.Cleanup(func() {
		if !store.Latched() {
			_ = store.Close()
		}
		_ = ownership.Close()
	})
	return repository, store, ownership
}

func mustCreateServer(t *testing.T, repository *Repository, namespace string, enabled bool) Server {
	t.Helper()
	created, err := repository.Create(context.Background(), CreateRequest{
		Definition:  Definition{Namespace: namespace, DisplayName: namespace, Enabled: enabled, Transport: testStdioTransport()},
		Idempotency: idempotency("create-"+namespace, "create-"+namespace, ""),
	})
	require.NoError(t, err)
	return created.Server
}

func idempotency(key, input, precondition string) *IdempotencyRequest {
	digest := sha256.Sum256([]byte(input))
	return &IdempotencyRequest{AuthorityID: testInstallationID, Method: "POST", Route: "/api/v1/test", Key: key, RequestHash: digest, Precondition: precondition}
}

func testStdioTransport() contract.StdioTransport {
	return contract.StdioTransport{
		Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp",
		Environment: map[string]string{}, SecretEnvironment: map[string]string{},
	}
}

func ptrReason(reason contract.PublicReason) *contract.PublicReason { return &reason }

func operationCount(t *testing.T, repository *Repository, serverID string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, repository.store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT count(*) FROM server_operations WHERE server_id = ?`, serverID).Scan(&count)
	}))
	return count
}

func seedIdempotencyCapacity(t *testing.T, repository *Repository, serverID string, now time.Time) {
	t.Helper()
	limit, ok := contract.FixedLimitByName("s2_idempotency_records")
	require.True(t, ok)
	require.NoError(t, repository.store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		if _, err := transaction.Exec(`DELETE FROM s2_idempotency`); err != nil {
			return err
		}
		for index := int64(0); index < limit.Maximum; index++ {
			digest := sha256.Sum256([]byte(strconv.FormatInt(index, 10)))
			if _, err := transaction.Exec(`INSERT INTO s2_idempotency (authority_id, method, route, idempotency_key, request_hash, precondition, result_kind, server_id, operation_id, desired_revision, result_json, created_at, expires_at) VALUES (?, 'POST', '/api/v1/test', ?, ?, '', 'server', ?, NULL, 1, '{"server":{}}', ?, ?)`, testInstallationID, "key-"+strconv.FormatInt(index, 10), digest[:], serverID, formatTime(now), formatTime(now.Add(contract.IdempotencyRetention))); err != nil {
				return err
			}
		}
		return nil
	}))
}

func idForSequence(sequence int64) string {
	return "01ARZ3NDEKTSV4RRFFQ69" + fmtFive(sequence)
}

func fmtFive(value int64) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	encoded := []byte("00000")
	for index := len(encoded) - 1; index >= 0; index-- {
		encoded[index] = alphabet[value%32]
		value /= 32
	}
	return string(encoded)
}

func TestErrorClassesAreDistinct(t *testing.T) {
	assert.False(t, errors.Is(ErrNotFound, ErrStaleRevision))
	assert.False(t, errors.Is(ErrIdempotencyConflict, ErrResourceLimit))
}
