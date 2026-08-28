package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/invocation"
)

type InvocationReader interface {
	List(context.Context, contract.InvocationListQuery) (contract.InvocationPage, error)
	Get(context.Context, string) (contract.Invocation, error)
}

func (handler *Handler) invocationsCollection(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	query, problem := parseInvocationQuery(request.URL.RawQuery)
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	page, err := handler.invocations.List(request.Context(), query)
	if err != nil {
		writeInvocationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *Handler) invocationMember(writer http.ResponseWriter, request *http.Request, invocationID string) {
	if request.Method != http.MethodGet {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	if request.URL.RawQuery != "" || !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	item, err := handler.invocations.Get(request.Context(), invocationID)
	if err != nil {
		writeInvocationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func parseInvocationQuery(rawQuery string) (contract.InvocationListQuery, contract.ProblemCode) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return contract.InvocationListQuery{}, contract.ProblemMalformedRequest
	}
	allowed := map[string]bool{
		"cursor": true, "limit": true, "principal_id": true, "server_id": true, "requested_name": true,
		"admission_class": true, "decision": true, "outcome": true,
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" || values[0] == "null" {
			return contract.InvocationListQuery{}, contract.ProblemMalformedRequest
		}
	}
	result := contract.InvocationListQuery{Limit: contract.AdminListPageDefault}
	if values, ok := query["limit"]; ok {
		limit, parseErr := strconv.Atoi(values[0])
		if parseErr != nil || limit < 1 || limit > limitValue("admin_list_page") {
			return contract.InvocationListQuery{}, contract.ProblemMalformedRequest
		}
		result.Limit = limit
	}
	if values, ok := query["cursor"]; ok {
		if len(values[0]) > limitValue("cursor_bytes") {
			return contract.InvocationListQuery{}, contract.ProblemInvalidCursor
		}
		result.Cursor = &values[0]
	}
	if values, ok := query["principal_id"]; ok {
		result.Filters.PrincipalID = &values[0]
	}
	if values, ok := query["server_id"]; ok {
		result.Filters.ServerID = &values[0]
	}
	if values, ok := query["requested_name"]; ok {
		result.Filters.RequestedName = &values[0]
	}
	if values, ok := query["admission_class"]; ok {
		value, parseErr := contract.ParseInvocationAdmissionClass(values[0])
		if parseErr != nil {
			return contract.InvocationListQuery{}, contract.ProblemMalformedRequest
		}
		result.Filters.AdmissionClass = &value
	}
	if values, ok := query["decision"]; ok {
		value, parseErr := contract.ParseAuthorizationDecision(values[0])
		if parseErr != nil {
			return contract.InvocationListQuery{}, contract.ProblemMalformedRequest
		}
		result.Filters.Decision = &value
	}
	if values, ok := query["outcome"]; ok {
		value, parseErr := contract.ParseInvocationOutcomeClass(values[0])
		if parseErr != nil {
			return contract.InvocationListQuery{}, contract.ProblemMalformedRequest
		}
		result.Filters.Outcome = &value
	}
	return result, ""
}

func writeInvocationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, invocation.ErrNotFound):
		writeProblem(writer, contract.ProblemNotFound)
	case errors.Is(err, invocation.ErrInvalidCursor):
		writeProblem(writer, contract.ProblemInvalidCursor)
	case errors.Is(err, invocation.ErrStaleCursor):
		writeProblem(writer, contract.ProblemStaleCursor)
	case errors.Is(err, invocation.ErrInvalidInput):
		writeProblem(writer, contract.ProblemMalformedRequest)
	default:
		writeProblem(writer, contract.ProblemStorageUnavailable)
	}
}
