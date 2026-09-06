package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

type ServerService interface {
	Create(context.Context, serverdomain.CreateRequest) (serverdomain.CreateResult, error)
	Get(context.Context, string) (serverdomain.Server, error)
	Patch(context.Context, string, string, serverdomain.Patch) (serverdomain.PatchResult, error)
	Delete(context.Context, string, string) (serverdomain.DeleteResult, error)
	ListServers(context.Context, *serverdomain.SnapshotCursor, int) (serverdomain.ServerPage, error)
	Authority(context.Context, string) (serverdomain.AuthorityMetadata, error)
	CreateOperation(context.Context, serverdomain.OperationRequest) (serverdomain.OperationResult, error)
	GetOperation(context.Context, string) (serverdomain.Operation, error)
	ListOperations(context.Context, string, *serverdomain.SnapshotCursor, int) (serverdomain.OperationPage, error)
}

type OperationStateProvider func(context.Context, string) serverdomain.OperationTriggerState

type rawServerCreate struct {
	Namespace   json.RawMessage `json:"namespace"`
	DisplayName json.RawMessage `json:"display_name"`
	Enabled     json.RawMessage `json:"enabled"`
	Transport   json.RawMessage `json:"transport"`
}

type rawServerPatch struct {
	DisplayName *string          `json:"display_name,omitempty"`
	Enabled     *bool            `json:"enabled,omitempty"`
	Transport   *json.RawMessage `json:"transport,omitempty"`
}

func (handler *Handler) serversCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		handler.listServers(writer, request)
	case http.MethodPost:
		handler.createServer(writer, request)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) serverMember(writer http.ResponseWriter, request *http.Request, serverID string) {
	switch request.Method {
	case http.MethodGet:
		handler.getServer(writer, request, serverID)
	case http.MethodPatch:
		handler.patchServer(writer, request, serverID)
	case http.MethodDelete:
		handler.deleteServer(writer, request, serverID)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) createServer(writer http.ResponseWriter, request *http.Request) {
	var raw rawServerCreate
	if !decodeStrictBody(writer, request, &raw) {
		return
	}
	var namespace, displayName string
	var enabled bool
	for _, member := range []struct {
		contents json.RawMessage
		field    contract.ServerConfigurationField
		decode   func() error
	}{
		{contents: raw.Namespace, field: contract.ServerConfigurationFieldNamespace, decode: func() error { return json.Unmarshal(raw.Namespace, &namespace) }},
		{contents: raw.DisplayName, field: contract.ServerConfigurationFieldDisplayName, decode: func() error { return json.Unmarshal(raw.DisplayName, &displayName) }},
		{contents: raw.Enabled, field: contract.ServerConfigurationFieldEnabled, decode: func() error { return json.Unmarshal(raw.Enabled, &enabled) }},
	} {
		if member.contents == nil {
			writeServerConfigurationError(writer, serverdomain.NewConfigurationError(member.field, contract.ServerConfigurationRuleRequired))
			return
		}
		if string(member.contents) == "null" || member.decode() != nil {
			writeServerConfigurationError(writer, serverdomain.NewConfigurationError(member.field, contract.ServerConfigurationRuleInvalid))
			return
		}
	}
	if raw.Transport == nil {
		writeServerConfigurationError(writer, serverdomain.NewConfigurationError(contract.ServerConfigurationFieldTransport, contract.ServerConfigurationRuleRequired))
		return
	}
	transport, err := serverdomain.DecodeTransport(raw.Transport)
	if err != nil {
		writeServerConfigurationError(writer, err)
		return
	}
	key, ok := idempotencyKey(writer, request)
	if !ok {
		return
	}
	authenticated, ok := request.Context().Value(authContextKey{}).(authentication)
	if !ok {
		writeProblem(writer, contract.ProblemAuthenticationRequired)
		return
	}
	definition := serverdomain.Definition{Namespace: namespace, DisplayName: displayName, Enabled: enabled, Transport: transport}
	canonical, _ := json.Marshal(contract.ServerCreate{Namespace: namespace, DisplayName: displayName, Enabled: enabled, Transport: transport})
	result, err := handler.servers.Create(request.Context(), serverdomain.CreateRequest{Definition: definition, Idempotency: &serverdomain.IdempotencyRequest{
		AuthorityID: authenticated.credential.ID, Method: request.Method, Route: "/api/v1/servers", Key: key, RequestHash: sha256.Sum256(canonical),
	}})
	if err != nil {
		writeServerError(writer, err)
		return
	}
	resource, err := handler.serverResource(request.Context(), result.Server)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	mutation := contract.ServerMutation{Server: resource, Operation: operationResource(result.Operation)}
	writer.Header().Set("ETag", contract.ServerETag(resource.ID, resource.DesiredRevision))
	writer.Header().Set("Location", "/api/v1/servers/"+resource.ID)
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	} else {
		handler.emit(contract.Invalidation{Kind: contract.InvalidationServers, ResourceID: &resource.ID})
		if result.Operation != nil {
			handler.emit(contract.Invalidation{Kind: contract.InvalidationServerOperations, ResourceID: &result.Operation.ID})
			handler.trigger(request.Context(), resource.ID, &result.Operation.ID, true)
		}
	}
	writeJSON(writer, status, mutation)
}

func (handler *Handler) getServer(writer http.ResponseWriter, request *http.Request, serverID string) {
	if !bodyless(request) || len(request.URL.Query()) != 0 {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	stored, err := handler.servers.Get(request.Context(), serverID)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	resource, err := handler.serverResource(request.Context(), stored)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	writer.Header().Set("ETag", contract.ServerETag(resource.ID, resource.DesiredRevision))
	writeJSON(writer, http.StatusOK, resource)
}

func (handler *Handler) patchServer(writer http.ResponseWriter, request *http.Request, serverID string) {
	var raw rawServerPatch
	if !decodeStrictBody(writer, request, &raw) {
		return
	}
	if raw.DisplayName == nil && raw.Enabled == nil && raw.Transport == nil {
		writeProblem(writer, contract.ProblemInvalidJSON)
		return
	}
	revision, ok := serverPrecondition(writer, request, serverID)
	if !ok {
		return
	}
	patch := serverdomain.Patch{DisplayName: raw.DisplayName, Enabled: raw.Enabled}
	if raw.Transport != nil {
		transport, err := serverdomain.DecodeTransport(*raw.Transport)
		if err != nil {
			writeServerConfigurationError(writer, err)
			return
		}
		patch.Transport = transport
	}
	result, err := handler.servers.Patch(request.Context(), serverID, revision, patch)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	resource, err := handler.serverResource(request.Context(), result.Server)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	writer.Header().Set("ETag", contract.ServerETag(resource.ID, resource.DesiredRevision))
	handler.emit(contract.Invalidation{Kind: contract.InvalidationServers, ResourceID: &resource.ID})
	if result.Operation != nil {
		if handler.authFlows != nil {
			handler.authFlows.FenceServer(serverID)
			handler.emit(contract.Invalidation{Kind: contract.InvalidationServerAuthFlows})
		}
		handler.emit(contract.Invalidation{Kind: contract.InvalidationServerOperations, ResourceID: &result.Operation.ID})
		handler.trigger(request.Context(), resource.ID, &result.Operation.ID, true)
	}
	writeJSON(writer, http.StatusOK, contract.ServerMutation{Server: resource, Operation: operationResource(result.Operation)})
}

func (handler *Handler) deleteServer(writer http.ResponseWriter, request *http.Request, serverID string) {
	if !decodeEmptyObject(writer, request) {
		return
	}
	revision, ok := serverPrecondition(writer, request, serverID)
	if !ok {
		return
	}
	result, err := handler.servers.Delete(request.Context(), serverID, revision)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	resource, err := handler.serverResource(request.Context(), result.Server)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	writer.Header().Set("ETag", contract.ServerETag(resource.ID, resource.DesiredRevision))
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	} else {
		handler.emit(contract.Invalidation{Kind: contract.InvalidationServers, ResourceID: &resource.ID})
		if result.Operation != nil {
			if handler.authFlows != nil {
				handler.authFlows.FenceServer(serverID)
				handler.emit(contract.Invalidation{Kind: contract.InvalidationServerAuthFlows})
			}
			handler.emit(contract.Invalidation{Kind: contract.InvalidationServerOperations, ResourceID: &result.Operation.ID})
			handler.trigger(request.Context(), resource.ID, &result.Operation.ID, true)
		}
	}
	writeJSON(writer, status, contract.ServerMutation{Server: resource, Operation: operationResource(result.Operation)})
}

func (handler *Handler) listServers(writer http.ResponseWriter, request *http.Request) {
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, cursor, problem := parseServerQuery(request.URL.Query())
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	page, err := handler.servers.ListServers(request.Context(), cursor, limit)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	items := make([]contract.Server, 0, len(page.Items))
	for _, stored := range page.Items {
		resource, resourceErr := handler.serverResource(request.Context(), stored)
		if resourceErr != nil {
			writeServerError(writer, resourceErr)
			return
		}
		items = append(items, resource)
	}
	var next *string
	if page.Next != nil {
		value := encodeServerCursor(*page.Next)
		next = &value
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.Server]{Items: items, NextCursor: next})
}

func (handler *Handler) serverResource(ctx context.Context, stored serverdomain.Server) (contract.Server, error) {
	authority, err := handler.servers.Authority(ctx, stored.ID)
	if err != nil {
		return contract.Server{}, err
	}
	var transport contract.Transport
	if stored.Transport != nil {
		transport, err = serverdomain.DecodeTransport(stored.Transport)
		if err != nil {
			return contract.Server{}, err
		}
	}
	credentialState := contract.ServerCredentialNotRequired
	if transportRequiresCredential(transport) {
		credentialState = contract.ServerCredentialAbsent
		if authority.CredentialRevisions.StaticCredential != "0" || authority.CredentialRevisions.OAuthTokens != "0" {
			credentialState = contract.ServerCredentialReady
		}
	}
	runtime := handler.runtimeStatus(stored.ID)
	if runtime.CredentialState != "" && (!transportRequiresCredential(transport) || runtime.CredentialState != contract.ServerCredentialNotRequired) {
		credentialState = runtime.CredentialState
	}
	durableCatalog := contract.ServerCatalog{DurableState: contract.DurableCatalogEmpty, ActiveState: runtime.CatalogState, Traversal: handler.catalogTraversal(stored.ID)}
	if handler.activeCatalog != nil {
		active := handler.activeCatalog.Status(stored.ID)
		durableCatalog.ActiveState = active.State
		durableCatalog.ActiveRevision = active.Revision
		durableCatalog.ActiveToolCount = active.ToolCount
	}
	if handler.catalog != nil {
		status, statusErr := handler.catalog.Status(ctx, stored.ID)
		if statusErr != nil {
			return contract.Server{}, statusErr
		}
		durableCatalog.DurableState = status.State
		durableCatalog.DurableRevision = status.Revision
		durableCatalog.DurableToolCount = status.ToolCount
		durableCatalog.LastSuccessAt = status.LastSuccessAt
	}
	if stored.DesiredState == contract.DesiredServerDeleted {
		runtime.State = contract.RuntimeDeleted
		runtime.Reason = nil
		runtime.RuntimeID = nil
		runtime.CredentialState = credentialState
		runtime.CatalogState = contract.ActiveCatalogAbsent
		durableCatalog.DurableState = contract.DurableCatalogRetired
		durableCatalog.ActiveState = contract.ActiveCatalogAbsent
		durableCatalog.ActiveRevision = nil
		durableCatalog.ActiveToolCount = 0
	}
	return contract.Server{
		ID: stored.ID, Namespace: stored.Namespace, DisplayName: stored.DisplayName, DesiredState: stored.DesiredState,
		DesiredRevision: stored.DesiredRevision, Transport: transport, CredentialRevisions: authority.CredentialRevisions,
		CredentialState: credentialState,
		Runtime:         contract.ServerRuntime{State: runtime.State, Reason: runtime.Reason, RuntimeID: runtime.RuntimeID, Reconciliation: runtime.Reconciliation, Dispatch: handler.dispatchStatus(stored.ID)},
		Catalog:         durableCatalog,
		CreatedAt:       stored.CreatedAt, UpdatedAt: stored.UpdatedAt, DeletedAt: stored.DeletedAt,
	}, nil
}

func (handler *Handler) trigger(ctx context.Context, serverID string, operationID *string, reset bool) {
	if handler.triggerServer != nil {
		handler.triggerServer(ctx, serverID, operationID, reset)
	}
}

func operationResource(operation *serverdomain.Operation) *contract.ServerOperation {
	if operation == nil {
		return nil
	}
	return &contract.ServerOperation{ID: operation.ID, ServerID: operation.ServerID, Kind: operation.Kind, TargetDesiredRevision: operation.TargetDesiredRevision, TargetCredentialRevisions: operation.TargetCredentialRevisions, State: operation.State, Reason: operation.Reason, CreatedAt: operation.CreatedAt, StartedAt: operation.StartedAt, FinishedAt: operation.FinishedAt}
}

func transportRequiresCredential(transport contract.Transport) bool {
	switch value := transport.(type) {
	case contract.StdioTransport:
		return len(value.SecretEnvironment) != 0
	case contract.StreamableHTTPTransport:
		return authenticationModeForTransport(value.Authentication) != contract.AuthenticationNone
	default:
		return false
	}
}

func authenticationModeForTransport(authentication contract.HTTPAuthentication) contract.AuthenticationMode {
	switch value := authentication.(type) {
	case contract.NoAuthentication:
		return value.Mode
	case contract.BearerAuthentication:
		return value.Mode
	case contract.OAuthAuthentication:
		return value.Mode
	default:
		return ""
	}
}

func serverPrecondition(writer http.ResponseWriter, request *http.Request, serverID string) (string, bool) {
	values := request.Header.Values("If-Match")
	if len(values) == 0 {
		writeProblem(writer, contract.ProblemPreconditionRequired)
		return "", false
	}
	if len(values) != 1 {
		writeProblem(writer, contract.ProblemStaleRevision)
		return "", false
	}
	prefix := `"server-` + serverID + `-`
	value := values[0]
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		writeProblem(writer, contract.ProblemStaleRevision)
		return "", false
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
	if revision == "" || (len(revision) > 1 && revision[0] == '0') {
		writeProblem(writer, contract.ProblemStaleRevision)
		return "", false
	}
	if _, err := strconv.ParseUint(revision, 10, 64); err != nil {
		writeProblem(writer, contract.ProblemStaleRevision)
		return "", false
	}
	return revision, true
}

func idempotencyKey(writer http.ResponseWriter, request *http.Request) (string, bool) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 || values[0] == "" || len(values[0]) > limitValue("idempotency_key_bytes") {
		writeProblem(writer, contract.ProblemInvalidIdempotencyKey)
		return "", false
	}
	for _, character := range values[0] {
		if character < 0x21 || character > 0x7e {
			writeProblem(writer, contract.ProblemInvalidIdempotencyKey)
			return "", false
		}
	}
	return values[0], true
}

func parseServerQuery(query url.Values) (int, *serverdomain.SnapshotCursor, contract.ProblemCode) {
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 {
			return 0, nil, contract.ProblemMalformedRequest
		}
	}
	limit := contract.S2ListPageDefault
	if text := query.Get("limit"); text != "" {
		value, err := strconv.Atoi(text)
		if err != nil || value < 1 || value > limitValue("s2_list_page") {
			return 0, nil, contract.ProblemMalformedRequest
		}
		limit = value
	}
	var cursor *serverdomain.SnapshotCursor
	if text := query.Get("cursor"); text != "" {
		if len(text) > limitValue("cursor_bytes") {
			return 0, nil, contract.ProblemInvalidCursor
		}
		contents, err := base64.RawURLEncoding.DecodeString(text)
		var decoded serverdomain.SnapshotCursor
		if err != nil || json.Unmarshal(contents, &decoded) != nil {
			return 0, nil, contract.ProblemInvalidCursor
		}
		cursor = &decoded
	}
	return limit, cursor, ""
}

func encodeServerCursor(cursor serverdomain.SnapshotCursor) string {
	contents, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(contents)
}

func limitStatus(name string) contract.LimitStatus {
	limit, _ := contract.FixedLimitByName(name)
	return contract.LimitStatus{Limit: limit.Maximum}
}

func writeServerConfigurationError(writer http.ResponseWriter, err error) {
	if code := admitProblem(writer, contract.ProblemInvalidServerConfiguration); code != contract.ProblemInvalidServerConfiguration {
		writeProblem(writer, code)
		return
	}
	context := contract.ServerConfigurationContext{Field: contract.ServerConfigurationFieldConfiguration, Rule: contract.ServerConfigurationRuleInvalid}
	var failure *serverdomain.ConfigurationError
	if errors.As(err, &failure) && contract.ValidServerConfigurationContext(failure.Context) {
		context = failure.Context
	}
	problem, _ := contract.ProblemForCode(contract.ProblemInvalidServerConfiguration)
	writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
	writer.WriteHeader(problem.Status)
	_ = json.NewEncoder(writer).Encode(contract.ServerConfigurationProblemEnvelope{ProblemEnvelope: contract.ProblemEnvelope(problem), Context: context})
}

func writeServerError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, serverdomain.ErrNotFound):
		writeProblem(writer, contract.ProblemNotFound)
	case errors.Is(err, serverdomain.ErrInvalidInput):
		writeServerConfigurationError(writer, err)
	case errors.Is(err, serverdomain.ErrIdentityUnavailable), errors.Is(err, serverdomain.ErrNamespaceUnavailable):
		writeProblem(writer, contract.ProblemNamespaceUnavailable)
	case errors.Is(err, serverdomain.ErrResourceLimit):
		writeProblem(writer, contract.ProblemResourceLimit)
	case errors.Is(err, serverdomain.ErrStaleRevision):
		writeProblem(writer, contract.ProblemStaleRevision)
	case errors.Is(err, serverdomain.ErrIdempotencyConflict):
		writeProblem(writer, contract.ProblemIdempotencyConflict)
	case errors.Is(err, serverdomain.ErrInvalidOperation):
		writeProblem(writer, contract.ProblemInvalidOperation)
	case errors.Is(err, serverdomain.ErrOperationConflict):
		writeProblem(writer, contract.ProblemOperationConflict)
	case errors.Is(err, serverdomain.ErrOAuthFlowActive):
		writeProblem(writer, contract.ProblemOAuthFlowActive)
	case errors.Is(err, serverdomain.ErrStaleCursor):
		writeProblem(writer, contract.ProblemStaleCursor)
	default:
		writeProblem(writer, contract.ProblemStorageUnavailable)
	}
}
