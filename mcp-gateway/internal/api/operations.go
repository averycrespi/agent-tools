package api

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

func (handler *Handler) operationsCollection(writer http.ResponseWriter, request *http.Request, serverID string) {
	switch request.Method {
	case http.MethodGet:
		handler.listOperations(writer, request, serverID)
	case http.MethodPost:
		handler.createOperation(writer, request, serverID)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) operationMember(writer http.ResponseWriter, request *http.Request, serverID, operationID string) {
	if request.Method != http.MethodGet || !bodyless(request) || len(request.URL.Query()) != 0 {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	operation, err := handler.servers.GetOperation(request.Context(), operationID)
	if err != nil || operation.ServerID != serverID {
		if err == nil {
			err = serverdomain.ErrNotFound
		}
		writeServerError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, operationResource(&operation))
}

func (handler *Handler) createOperation(writer http.ResponseWriter, request *http.Request, serverID string) {
	var input contract.ServerOperationCreate
	if !decodeStrictBody(writer, request, &input) {
		return
	}
	if _, err := contract.ParseExplicitServerOperationKind(string(input.Kind)); err != nil {
		writeProblem(writer, contract.ProblemInvalidOperation)
		return
	}
	revision, ok := serverPrecondition(writer, request, serverID)
	if !ok {
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
	canonical, _ := json.Marshal(input)
	precondition := contract.ServerETag(serverID, revision)
	result, err := handler.servers.CreateOperation(request.Context(), serverdomain.OperationRequest{
		ServerID:                serverID,
		Kind:                    input.Kind,
		ExpectedDesiredRevision: revision,
		Idempotency: &serverdomain.IdempotencyRequest{
			AuthorityID:  authenticated.credential.ID,
			Method:       request.Method,
			Route:        "/api/v1/servers/" + serverID + "/operations",
			Key:          key,
			RequestHash:  sha256.Sum256(canonical),
			Precondition: precondition,
		},
		TriggerState: operationStatePointer(handler.operationState(request.Context(), serverID)),
	})
	if err != nil {
		writeServerError(writer, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	} else {
		handler.emit(contract.Invalidation{Kind: contract.InvalidationServerOperations, ResourceID: &result.Operation.ID})
		handler.trigger(serverID, &result.Operation.ID, input.Kind == contract.OperationRetry)
	}
	writeJSON(writer, status, contract.ServerOperationMutation{Operation: *operationResource(&result.Operation)})
}

func (handler *Handler) listOperations(writer http.ResponseWriter, request *http.Request, serverID string) {
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, cursor, problem := parseServerQuery(request.URL.Query())
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	page, err := handler.servers.ListOperations(request.Context(), serverID, cursor, limit)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	items := make([]contract.ServerOperation, 0, len(page.Items))
	for index := range page.Items {
		items = append(items, *operationResource(&page.Items[index]))
	}
	var next *string
	if page.Next != nil {
		value := encodeServerCursor(*page.Next)
		next = &value
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.ServerOperation]{Items: items, NextCursor: next})
}

func operationStatePointer(state serverdomain.OperationTriggerState) *serverdomain.OperationTriggerState {
	return &state
}
