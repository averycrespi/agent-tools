package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

type AuthFlowService interface {
	CreateInitial(context.Context, string, string) (contract.AuthFlowCreation, error)
	Get(context.Context, string, string) (contract.ServerAuthFlow, error)
	List(context.Context, string, *serverdomain.SnapshotCursor, int) ([]contract.ServerAuthFlow, *serverdomain.SnapshotCursor, error)
	Cancel(context.Context, string, string) (contract.ServerAuthFlow, error)
	FenceServer(string)
}

func (handler *Handler) authFlowsCollection(writer http.ResponseWriter, request *http.Request, serverID string) {
	switch request.Method {
	case http.MethodGet:
		handler.listAuthFlows(writer, request, serverID)
	case http.MethodPost:
		handler.createAuthFlow(writer, request, serverID)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) authFlowMember(writer http.ResponseWriter, request *http.Request, serverID, flowID string) {
	switch request.Method {
	case http.MethodGet:
		handler.getAuthFlow(writer, request, serverID, flowID)
	case http.MethodDelete:
		handler.deleteAuthFlow(writer, request, serverID, flowID)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) createAuthFlow(writer http.ResponseWriter, request *http.Request, serverID string) {
	if !decodeEmptyObject(writer, request) {
		return
	}
	revision, ok := serverPrecondition(writer, request, serverID)
	if !ok {
		return
	}
	creation, err := handler.authFlows.CreateInitial(request.Context(), serverID, revision)
	if err != nil {
		correlationID := oauth.FailureCorrelationID(err)
		if correlationID != "" {
			writer.Header().Set(contract.OAuthCorrelationHeader, correlationID)
			handler.emit(contract.Invalidation{Kind: contract.InvalidationServerAuthFlows, ResourceID: &correlationID})
		} else {
			handler.emit(contract.Invalidation{Kind: contract.InvalidationServerAuthFlows})
		}
		writeAuthFlowError(writer, err)
		return
	}
	handler.emit(contract.Invalidation{Kind: contract.InvalidationServerAuthFlows, ResourceID: &creation.Flow.ID})
	writeJSON(writer, http.StatusCreated, creation)
}

func (handler *Handler) listAuthFlows(writer http.ResponseWriter, request *http.Request, serverID string) {
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, cursor, problem := parseServerQuery(request.URL.Query())
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	items, nextCursor, err := handler.authFlows.List(request.Context(), serverID, cursor, limit)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	var next *string
	if nextCursor != nil {
		value := encodeServerCursor(*nextCursor)
		next = &value
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.ServerAuthFlow]{Items: items, NextCursor: next})
}

func (handler *Handler) getAuthFlow(writer http.ResponseWriter, request *http.Request, serverID, flowID string) {
	if !bodyless(request) || len(request.URL.Query()) != 0 {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	flow, err := handler.authFlows.Get(request.Context(), serverID, flowID)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, flow)
}

func writeAuthFlowError(writer http.ResponseWriter, err error) {
	if errors.Is(err, oauth.ErrFlowRejected) {
		writeProblem(writer, contract.ProblemDownstreamUnavailable)
		return
	}
	writeServerError(writer, err)
}

func (handler *Handler) deleteAuthFlow(writer http.ResponseWriter, request *http.Request, serverID, flowID string) {
	if !decodeEmptyObject(writer, request) {
		return
	}
	_, err := handler.authFlows.Cancel(request.Context(), serverID, flowID)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	handler.emit(contract.Invalidation{Kind: contract.InvalidationServerAuthFlows, ResourceID: &flowID})
	writer.WriteHeader(http.StatusNoContent)
}
