package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type rawGrantCreate struct {
	PrincipalID  json.RawMessage `json:"principal_id"`
	Effect       json.RawMessage `json:"effect"`
	ServerID     json.RawMessage `json:"server_id"`
	UpstreamName json.RawMessage `json:"upstream_name"`
	Constraint   json.RawMessage `json:"constraint"`
	ExpiresAt    json.RawMessage `json:"expires_at"`
}

func (handler *Handler) grantsCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		handler.listGrants(writer, request)
	case http.MethodPost:
		handler.createGrant(writer, request)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) grantMember(writer http.ResponseWriter, request *http.Request, grantID string) {
	switch request.Method {
	case http.MethodGet:
		if !bodyless(request) || request.URL.RawQuery != "" {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
		grant, err := handler.principals.GetGrant(request.Context(), grantID)
		if err != nil {
			writeGrantError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, grant)
	case http.MethodDelete:
		if !bodyless(request) || request.URL.RawQuery != "" {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
		if err := handler.principals.DeleteGrant(request.Context(), grantID); err != nil {
			writeGrantError(writer, err)
			return
		}
		handler.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
		writer.WriteHeader(http.StatusNoContent)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) createGrant(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	var raw rawGrantCreate
	if !decodeStrictBody(writer, request, &raw) {
		return
	}
	grantRequest, ok := decodeGrantCreate(writer, raw)
	if !ok {
		return
	}
	if handler.grantTarget == nil {
		writeProblem(writer, contract.ProblemAuthorizationUnavailable)
		return
	}
	grant, err := handler.principals.CreateGrant(request.Context(), grantRequest, handler.grantTarget)
	if err != nil {
		writeGrantError(writer, err)
		return
	}
	handler.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
	writeJSON(writer, http.StatusCreated, grant)
}

func decodeGrantCreate(writer http.ResponseWriter, raw rawGrantCreate) (authorization.CreateGrantRequest, bool) {
	var request authorization.CreateGrantRequest
	if !decodeRequiredGrantMember(raw.PrincipalID, &request.PrincipalID) ||
		!decodeRequiredGrantMember(raw.Effect, &request.Effect) ||
		!decodeRequiredGrantMember(raw.ServerID, &request.ServerID) ||
		!decodeNullableGrantMember(raw.UpstreamName, &request.UpstreamName) {
		writeProblem(writer, contract.ProblemInvalidGrant)
		return authorization.CreateGrantRequest{}, false
	}
	if raw.Constraint == nil {
		writeProblem(writer, contract.ProblemInvalidGrant)
		return authorization.CreateGrantRequest{}, false
	}
	if !bytes.Equal(raw.Constraint, []byte("null")) {
		value := json.RawMessage(append([]byte(nil), raw.Constraint...))
		request.Constraint = &value
	}
	var expiresAt *string
	if !decodeNullableGrantMember(raw.ExpiresAt, &expiresAt) {
		writeProblem(writer, contract.ProblemInvalidGrant)
		return authorization.CreateGrantRequest{}, false
	}
	if expiresAt != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *expiresAt)
		if err != nil || parsed.UTC().Format(time.RFC3339Nano) != *expiresAt {
			writeProblem(writer, contract.ProblemInvalidGrant)
			return authorization.CreateGrantRequest{}, false
		}
		request.ExpiresAt = &parsed
	}
	return request, true
}

func decodeRequiredGrantMember[T any](raw json.RawMessage, destination *T) bool {
	return raw != nil && !bytes.Equal(raw, []byte("null")) && json.Unmarshal(raw, destination) == nil
}

func decodeNullableGrantMember[T any](raw json.RawMessage, destination **T) bool {
	if raw == nil {
		return false
	}
	if bytes.Equal(raw, []byte("null")) {
		return true
	}
	var value T
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	*destination = &value
	return true
}

func (handler *Handler) listGrants(writer http.ResponseWriter, request *http.Request) {
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, filter, cursor, problem := parseGrantQuery(request.URL.RawQuery)
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	page, err := handler.principals.ListGrants(request.Context(), filter, cursor, limit)
	if err != nil {
		writeGrantError(writer, err)
		return
	}
	var next *string
	if page.Next != nil {
		value := encodePrincipalCursor(*page.Next)
		next = &value
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.Grant]{Items: page.Items, NextCursor: next})
}

func parseGrantQuery(rawQuery string) (int, authorization.GrantFilter, *authorization.SnapshotCursor, contract.ProblemCode) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, authorization.GrantFilter{}, nil, contract.ProblemMalformedRequest
	}
	for key, values := range query {
		if (key != "cursor" && key != "limit" && key != "principal_id" && key != "server_id") || len(values) != 1 || values[0] == "" || values[0] == "null" {
			return 0, authorization.GrantFilter{}, nil, contract.ProblemMalformedRequest
		}
	}
	limit := contract.S3ListPageDefault
	if values, ok := query["limit"]; ok {
		value, parseErr := strconv.Atoi(values[0])
		if parseErr != nil || value < 1 || value > limitValue("admin_list_page") {
			return 0, authorization.GrantFilter{}, nil, contract.ProblemMalformedRequest
		}
		limit = value
	}
	filter := authorization.GrantFilter{}
	if values, ok := query["principal_id"]; ok {
		filter.PrincipalID = values[0]
	}
	if values, ok := query["server_id"]; ok {
		filter.ServerID = values[0]
	}
	var cursor *authorization.SnapshotCursor
	if values, ok := query["cursor"]; ok {
		if len(values[0]) > limitValue("cursor_bytes") {
			return 0, authorization.GrantFilter{}, nil, contract.ProblemInvalidCursor
		}
		contents, decodeErr := base64.RawURLEncoding.DecodeString(values[0])
		var decoded authorization.SnapshotCursor
		if decodeErr != nil || strictjson.DecodeReader(bytes.NewReader(contents), &decoded, strictjson.Options{MaxBytes: int64(limitValue("cursor_bytes")), MaxDepth: limitValue("json_depth"), RejectUnknownMembers: true}) != nil {
			return 0, authorization.GrantFilter{}, nil, contract.ProblemInvalidCursor
		}
		cursor = &decoded
	}
	return limit, filter, cursor, ""
}

func writeGrantError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		writeProblem(writer, contract.ProblemNotFound)
	case errors.Is(err, authorization.ErrInvalidInput):
		writeProblem(writer, contract.ProblemInvalidGrant)
	case errors.Is(err, authorization.ErrResourceLimit):
		writeProblem(writer, contract.ProblemResourceLimit)
	case errors.Is(err, authorization.ErrStaleCursor):
		writeProblem(writer, contract.ProblemStaleCursor)
	case errors.Is(err, authorization.ErrShuttingDown):
		writeProblem(writer, contract.ProblemShuttingDown)
	default:
		writeProblem(writer, contract.ProblemAuthorizationUnavailable)
	}
}
