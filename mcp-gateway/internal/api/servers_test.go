package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeServerService struct {
	server           serverdomain.Server
	create           serverdomain.CreateRequest
	patch            serverdomain.Patch
	operationRequest serverdomain.OperationRequest
	operations       []serverdomain.Operation
	operationReplay  bool
	created          bool
}

func (service *fakeServerService) Create(_ context.Context, request serverdomain.CreateRequest) (serverdomain.CreateResult, error) {
	service.create = request
	if service.created {
		return serverdomain.CreateResult{Server: service.server, Replayed: true}, nil
	}
	service.created = true
	service.server = storedServer(request.Definition)
	return serverdomain.CreateResult{Server: service.server}, nil
}

func (service *fakeServerService) Get(_ context.Context, id string) (serverdomain.Server, error) {
	if service.server.ID != id {
		return serverdomain.Server{}, serverdomain.ErrNotFound
	}
	return service.server, nil
}

func (service *fakeServerService) Patch(_ context.Context, id, revision string, patch serverdomain.Patch) (serverdomain.PatchResult, error) {
	if service.server.ID != id {
		return serverdomain.PatchResult{}, serverdomain.ErrNotFound
	}
	if revision != service.server.DesiredRevision {
		return serverdomain.PatchResult{}, serverdomain.ErrStaleRevision
	}
	service.patch = patch
	service.server.DesiredRevision = "2"
	if patch.DisplayName != nil {
		service.server.DisplayName = *patch.DisplayName
	}
	result := serverdomain.PatchResult{Server: service.server}
	if patch.Enabled != nil {
		if *patch.Enabled {
			service.server.DesiredState = contract.DesiredServerEnabled
		} else {
			service.server.DesiredState = contract.DesiredServerDisabled
		}
		result.Server = service.server
		operation := serverdomain.Operation{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", ServerID: id, Kind: contract.OperationActivate, TargetDesiredRevision: "2", TargetCredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}, State: contract.OperationScheduled, CreatedAt: "2026-08-23T00:00:00Z"}
		result.Operation = &operation
	}
	return result, nil
}

func (service *fakeServerService) Delete(_ context.Context, id, revision string) (serverdomain.DeleteResult, error) {
	if service.server.ID != id {
		return serverdomain.DeleteResult{}, serverdomain.ErrNotFound
	}
	if revision != service.server.DesiredRevision {
		return serverdomain.DeleteResult{}, serverdomain.ErrStaleRevision
	}
	service.server.DesiredRevision = "3"
	service.server.DesiredState = contract.DesiredServerDeleted
	service.server.Transport = nil
	deleted := "2026-08-23T00:00:00Z"
	service.server.DeletedAt = &deleted
	return serverdomain.DeleteResult{Server: service.server}, nil
}

func (service *fakeServerService) ListServers(context.Context, *serverdomain.SnapshotCursor, int) (serverdomain.ServerPage, error) {
	return serverdomain.ServerPage{Items: []serverdomain.Server{service.server}}, nil
}

func (service *fakeServerService) Authority(context.Context, string) (serverdomain.AuthorityMetadata, error) {
	return serverdomain.AuthorityMetadata{RegistrationRevision: "0", CredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}}, nil
}
func (service *fakeServerService) CreateOperation(_ context.Context, request serverdomain.OperationRequest) (serverdomain.OperationResult, error) {
	service.operationRequest = request
	if service.operationReplay && len(service.operations) != 0 {
		return serverdomain.OperationResult{Operation: service.operations[0], Replayed: true}, nil
	}
	operation := serverdomain.Operation{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", ServerID: request.ServerID, Kind: request.Kind, TargetDesiredRevision: request.ExpectedDesiredRevision, TargetCredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}, State: contract.OperationScheduled, CreatedAt: "2026-08-23T00:00:00Z"}
	service.operations = append(service.operations, operation)
	service.operationReplay = true
	return serverdomain.OperationResult{Operation: operation}, nil
}
func (service *fakeServerService) GetOperation(_ context.Context, id string) (serverdomain.Operation, error) {
	for _, operation := range service.operations {
		if operation.ID == id {
			return operation, nil
		}
	}
	return serverdomain.Operation{}, serverdomain.ErrNotFound
}
func (service *fakeServerService) ListOperations(context.Context, string, *serverdomain.SnapshotCursor, int) (serverdomain.OperationPage, error) {
	return serverdomain.OperationPage{Items: append([]serverdomain.Operation(nil), service.operations...)}, nil
}

func storedServer(definition serverdomain.Definition) serverdomain.Server {
	transport, _ := jsonMarshal(definition.Transport)
	state := contract.DesiredServerDisabled
	if definition.Enabled {
		state = contract.DesiredServerEnabled
	}
	return serverdomain.Server{ID: testID, Namespace: definition.Namespace, DisplayName: definition.DisplayName, DesiredState: state, DesiredRevision: "1", Transport: transport, CreatedAt: "2026-08-23T00:00:00Z", UpdatedAt: "2026-08-23T00:00:00Z"}
}

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }

func newServerTestHandler(t *testing.T, service ServerService) http.Handler {
	return newServerTestHandlerWithTrigger(t, service, nil)
}

func newServerTestHandlerWithTrigger(t *testing.T, service ServerService, trigger func(string, *string, bool)) http.Handler {
	t.Helper()
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Servers: service, TriggerServer: trigger})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	return boundary
}

func TestServerCreateReplayGetAndDisplayPatch(t *testing.T) {
	service := new(fakeServerService)
	handler := newServerTestHandler(t, service)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "create-alpha"}
	body := `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`
	created := perform(handler, http.MethodPost, "/api/v1/servers", body, headers)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	assert.Equal(t, contract.ServerETag(testID, "1"), created.Header().Get("ETag"))
	assert.Equal(t, "/api/v1/servers/"+testID, created.Header().Get("Location"))
	assert.Contains(t, created.Body.String(), `"operation":null`)
	assert.Equal(t, testID, service.create.Idempotency.AuthorityID)

	replay := perform(handler, http.MethodPost, "/api/v1/servers", body, headers)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())

	got := perform(handler, http.MethodGet, "/api/v1/servers/"+testID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	assert.Contains(t, got.Body.String(), `"credential_revisions":{"static_credential":"0","oauth_client":"0","oauth_tokens":"0"}`)
	assert.Contains(t, got.Body.String(), `"active_state":"absent"`)

	patch := perform(handler, http.MethodPatch, "/api/v1/servers/"+testID, `{"display_name":"Renamed"}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.ServerETag(testID, "1")})
	require.Equal(t, http.StatusOK, patch.Code, patch.Body.String())
	assert.Equal(t, contract.ServerETag(testID, "2"), patch.Header().Get("ETag"))
	assert.Contains(t, patch.Body.String(), `"operation":null`)
}

func TestServerResourceUsesSafeProcessLocalRuntimeStatus(t *testing.T) {
	service := new(fakeServerService)
	service.server = storedServer(serverdomain.Definition{Namespace: "alpha", DisplayName: "Alpha", Enabled: true, Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}})
	reason := contract.ReasonConnectivity
	runtimeID := "runtime-safe-id"
	handler := New(Options{
		Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Servers: service,
		RuntimeStatus: func(string) RuntimeStatus {
			return RuntimeStatus{State: contract.RuntimeRetryWait, Reason: &reason, RuntimeID: &runtimeID, CredentialState: contract.ServerCredentialUnavailable, CatalogState: contract.ActiveCatalogStale, Reconciliation: contract.LimitStatus{InUse: 1, Limit: 1, Saturated: true}}
		},
	})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	response := perform(boundary, http.MethodGet, "/api/v1/servers/"+testID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"state":"retry_wait","reason":"connectivity","runtime_id":"runtime-safe-id"`)
	assert.Contains(t, response.Body.String(), `"reconciliation":{"in_use":1,"limit":1,"saturated":true}`)
	assert.Contains(t, response.Body.String(), `"credential_state":"unavailable"`)
	assert.Contains(t, response.Body.String(), `"active_state":"stale"`)
}

func TestServerMutationsTriggerReconciliationOnlyForBehavioralWork(t *testing.T) {
	transport := contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}
	var triggered []string
	trigger := func(serverID string, operationID *string, reset bool) {
		require.NotNil(t, operationID)
		assert.True(t, reset)
		triggered = append(triggered, serverID)
	}

	displayService := new(fakeServerService)
	displayService.server = storedServer(serverdomain.Definition{Namespace: "alpha", DisplayName: "Alpha", Transport: transport})
	displayHandler := newServerTestHandlerWithTrigger(t, displayService, trigger)
	display := perform(displayHandler, http.MethodPatch, "/api/v1/servers/"+testID, `{"display_name":"Renamed"}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.ServerETag(testID, "1")})
	require.Equal(t, http.StatusOK, display.Code, display.Body.String())
	assert.Empty(t, triggered)

	behaviorService := new(fakeServerService)
	behaviorService.server = storedServer(serverdomain.Definition{Namespace: "alpha", DisplayName: "Alpha", Transport: transport})
	behaviorHandler := newServerTestHandlerWithTrigger(t, behaviorService, trigger)
	behavioral := perform(behaviorHandler, http.MethodPatch, "/api/v1/servers/"+testID, `{"enabled":true}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.ServerETag(testID, "1")})
	require.Equal(t, http.StatusOK, behavioral.Code, behavioral.Body.String())
	assert.Equal(t, []string{testID}, triggered)
}

func TestServerOperationCreateReplayReadAndList(t *testing.T) {
	service := new(fakeServerService)
	service.server = storedServer(serverdomain.Definition{Namespace: "alpha", DisplayName: "Alpha", Enabled: true, Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}})
	handler := newServerTestHandler(t, service)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "reload", "If-Match": contract.ServerETag(testID, "1")}
	created := perform(handler, http.MethodPost, "/api/v1/servers/"+testID+"/operations", `{"kind":"reload"}`, headers)
	require.Equal(t, http.StatusAccepted, created.Code, created.Body.String())
	assert.Contains(t, created.Body.String(), `"kind":"reload"`)
	assert.Equal(t, testID, service.operationRequest.Idempotency.AuthorityID)
	assert.Equal(t, contract.RuntimeInactive, service.operationRequest.TriggerState.RuntimeState)

	replayed := perform(handler, http.MethodPost, "/api/v1/servers/"+testID+"/operations", `{"kind":"reload"}`, headers)
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())

	operationID := service.operations[0].ID
	got := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/operations/"+operationID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	listed := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/operations?limit=1", "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.Contains(t, listed.Body.String(), operationID)

	invalid := perform(handler, http.MethodPost, "/api/v1/servers/"+testID+"/operations", `{"kind":"activate"}`, headers)
	assert.Equal(t, http.StatusBadRequest, invalid.Code)
	assert.Contains(t, invalid.Body.String(), "invalid_operation")
	wrongParent := perform(handler, http.MethodGet, "/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAX/operations/"+operationID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	assert.Equal(t, http.StatusNotFound, wrongParent.Code)
}

func TestServerCreateRequiresCompleteConfiguration(t *testing.T) {
	tests := map[string]string{
		"enabled":                  `{"namespace":"alpha","display_name":"Alpha","transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`,
		"stdio arguments":          `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"stdio","executable":"/bin/true","working_directory":"/tmp","environment":{},"secret_environment":{}}}`,
		"stdio environment":        `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","secret_environment":{}}}`,
		"stdio secret environment": `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{}}}`,
		"OAuth trusted origins":    `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"auto","authentication":{"mode":"oauth","registration":{"mode":"dynamic","issuer":null},"request_offline_access":false}}}`,
		"OAuth offline access":     `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"auto","authentication":{"mode":"oauth","registration":{"mode":"dynamic","issuer":null},"trusted_origins":[]}}}`,
		"dynamic OAuth issuer":     `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"auto","authentication":{"mode":"oauth","registration":{"mode":"dynamic"},"trusted_origins":[],"request_offline_access":false}}}`,
		"static OAuth issuer":      `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"auto","authentication":{"mode":"oauth","registration":{"mode":"static","client_id":"client","token_endpoint_auth_method":"none"},"trusted_origins":[],"request_offline_access":false}}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			service := new(fakeServerService)
			handler := newServerTestHandler(t, service)
			response := perform(handler, http.MethodPost, "/api/v1/servers", body, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "complete"})
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Contains(t, response.Body.String(), "invalid_server_configuration")
			assert.False(t, service.created)
		})
	}
}

func TestServerRequestValidationAndPreconditions(t *testing.T) {
	service := new(fakeServerService)
	handler := newServerTestHandler(t, service)
	authJSON := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}

	missingKey := perform(handler, http.MethodPost, "/api/v1/servers", `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`, authJSON)
	assert.Equal(t, http.StatusBadRequest, missingKey.Code)
	assert.Contains(t, missingKey.Body.String(), "invalid_idempotency_key")

	unknownNested := perform(handler, http.MethodPost, "/api/v1/servers", `{"namespace":"alpha","display_name":"Alpha","enabled":false,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{},"secret":"canary"}}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "x"})
	assert.Equal(t, http.StatusBadRequest, unknownNested.Code)
	assert.Contains(t, unknownNested.Body.String(), "invalid_server_configuration")
	assert.False(t, service.created)

	service.server = storedServer(serverdomain.Definition{Namespace: "alpha", DisplayName: "Alpha", Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}})
	missingPrecondition := perform(handler, http.MethodPatch, "/api/v1/servers/"+testID, `{"display_name":"Renamed"}`, authJSON)
	assert.Equal(t, http.StatusPreconditionRequired, missingPrecondition.Code)
	stale := perform(handler, http.MethodPatch, "/api/v1/servers/"+testID, `{"display_name":"Renamed"}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": `W/` + contract.ServerETag(testID, "1")})
	assert.Equal(t, http.StatusPreconditionFailed, stale.Code)
	assert.True(t, strings.Contains(stale.Body.String(), "stale_revision"))
}
