package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type PrincipalService interface {
	CreatePrincipal(context.Context, authorization.CreatePrincipalRequest) (contract.PrincipalCreation, error)
	GetPrincipal(context.Context, string) (contract.Principal, error)
	ListPrincipals(context.Context, *authorization.SnapshotCursor, int) (authorization.PrincipalPage, error)
	PatchPrincipal(context.Context, string, authorization.PatchPrincipalRequest) (contract.Principal, error)
}

type rawPrincipalCreate struct {
	DisplayName *string                       `json:"display_name"`
	Visibility  *contract.PrincipalVisibility `json:"visibility"`
}

type rawPrincipalPatch struct {
	DisplayName json.RawMessage `json:"display_name,omitempty"`
	State       json.RawMessage `json:"state,omitempty"`
	Visibility  json.RawMessage `json:"visibility,omitempty"`
}

func (handler *Handler) principalsCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		handler.listPrincipals(writer, request)
	case http.MethodPost:
		handler.createPrincipal(writer, request)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) principalMember(writer http.ResponseWriter, request *http.Request, principalID string) {
	switch request.Method {
	case http.MethodGet:
		handler.getPrincipal(writer, request, principalID)
	case http.MethodPatch:
		handler.patchPrincipal(writer, request, principalID)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func (handler *Handler) createPrincipal(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	var raw rawPrincipalCreate
	if !decodeStrictBody(writer, request, &raw) {
		return
	}
	if raw.DisplayName == nil || raw.Visibility == nil {
		writeProblem(writer, contract.ProblemInvalidPrincipal)
		return
	}
	created, err := handler.principals.CreatePrincipal(request.Context(), authorization.CreatePrincipalRequest{DisplayName: *raw.DisplayName, Visibility: *raw.Visibility})
	if err != nil {
		writePrincipalError(writer, err)
		return
	}
	writer.Header().Set("ETag", contract.PrincipalETag(created.Principal.ID, created.Principal.Revision))
	handler.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
	writeJSON(writer, http.StatusCreated, created)
}

func (handler *Handler) getPrincipal(writer http.ResponseWriter, request *http.Request, principalID string) {
	if !bodyless(request) || request.URL.RawQuery != "" {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	principal, err := handler.principals.GetPrincipal(request.Context(), principalID)
	if err != nil {
		writePrincipalError(writer, err)
		return
	}
	writer.Header().Set("ETag", contract.PrincipalETag(principal.ID, principal.Revision))
	writeJSON(writer, http.StatusOK, principal)
}

func (handler *Handler) patchPrincipal(writer http.ResponseWriter, request *http.Request, principalID string) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	var raw rawPrincipalPatch
	if !decodeStrictBody(writer, request, &raw) {
		return
	}
	if raw.DisplayName == nil && raw.State == nil && raw.Visibility == nil {
		writeProblem(writer, contract.ProblemInvalidPrincipal)
		return
	}
	patch := authorization.PatchPrincipalRequest{}
	if !decodePrincipalPatchMember(writer, raw.DisplayName, &patch.DisplayName) ||
		!decodePrincipalPatchMember(writer, raw.State, &patch.State) ||
		!decodePrincipalPatchMember(writer, raw.Visibility, &patch.Visibility) {
		return
	}
	revision, ok := principalPrecondition(writer, request, principalID)
	if !ok {
		return
	}
	patch.ExpectedRevision = revision
	principal, err := handler.principals.PatchPrincipal(request.Context(), principalID, patch)
	if err != nil {
		writePrincipalError(writer, err)
		return
	}
	writer.Header().Set("ETag", contract.PrincipalETag(principal.ID, principal.Revision))
	handler.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
	writeJSON(writer, http.StatusOK, principal)
}

func decodePrincipalPatchMember[T any](writer http.ResponseWriter, raw json.RawMessage, destination **T) bool {
	if raw == nil {
		return true
	}
	if bytes.Equal(raw, []byte("null")) {
		writeProblem(writer, contract.ProblemInvalidPrincipal)
		return false
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		writeProblem(writer, contract.ProblemInvalidPrincipal)
		return false
	}
	*destination = &value
	return true
}

func (handler *Handler) listPrincipals(writer http.ResponseWriter, request *http.Request) {
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, cursor, problem := parsePrincipalQuery(request.URL.RawQuery)
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	page, err := handler.principals.ListPrincipals(request.Context(), cursor, limit)
	if err != nil {
		writePrincipalError(writer, err)
		return
	}
	var next *string
	if page.Next != nil {
		encoded := encodePrincipalCursor(*page.Next)
		next = &encoded
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.Principal]{Items: page.Items, NextCursor: next})
}

func parsePrincipalQuery(rawQuery string) (int, *authorization.SnapshotCursor, contract.ProblemCode) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, nil, contract.ProblemMalformedRequest
	}
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 || values[0] == "" || values[0] == "null" {
			return 0, nil, contract.ProblemMalformedRequest
		}
	}
	limit := contract.S3ListPageDefault
	if values, ok := query["limit"]; ok {
		value, parseErr := strconv.Atoi(values[0])
		if parseErr != nil || value < 1 || value > limitValue("admin_list_page") {
			return 0, nil, contract.ProblemMalformedRequest
		}
		limit = value
	}
	var cursor *authorization.SnapshotCursor
	if values, ok := query["cursor"]; ok {
		text := values[0]
		if len(text) > limitValue("cursor_bytes") {
			return 0, nil, contract.ProblemInvalidCursor
		}
		contents, err := base64.RawURLEncoding.DecodeString(text)
		var decoded authorization.SnapshotCursor
		if err != nil || strictjson.DecodeReader(bytes.NewReader(contents), &decoded, strictjson.Options{MaxBytes: int64(limitValue("cursor_bytes")), MaxDepth: limitValue("json_depth"), RejectUnknownMembers: true}) != nil {
			return 0, nil, contract.ProblemInvalidCursor
		}
		cursor = &decoded
	}
	return limit, cursor, ""
}

func encodePrincipalCursor(cursor authorization.SnapshotCursor) string {
	contents, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(contents)
}

func principalPrecondition(writer http.ResponseWriter, request *http.Request, principalID string) (string, bool) {
	values := request.Header.Values("If-Match")
	if len(values) == 0 {
		writeProblem(writer, contract.ProblemPrincipalPreconditionRequired)
		return "", false
	}
	if len(values) != 1 {
		writeProblem(writer, contract.ProblemStalePrincipalRevision)
		return "", false
	}
	prefix := `"principal-` + principalID + `-`
	value := values[0]
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		writeProblem(writer, contract.ProblemStalePrincipalRevision)
		return "", false
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
	if revision == "" || (len(revision) > 1 && revision[0] == '0') {
		writeProblem(writer, contract.ProblemStalePrincipalRevision)
		return "", false
	}
	if _, err := strconv.ParseUint(revision, 10, 64); err != nil {
		writeProblem(writer, contract.ProblemStalePrincipalRevision)
		return "", false
	}
	return revision, true
}

func writePrincipalError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		writeProblem(writer, contract.ProblemNotFound)
	case errors.Is(err, authorization.ErrInvalidInput):
		writeProblem(writer, contract.ProblemInvalidPrincipal)
	case errors.Is(err, authorization.ErrResourceLimit):
		writeProblem(writer, contract.ProblemResourceLimit)
	case errors.Is(err, authorization.ErrStaleRevision):
		writeProblem(writer, contract.ProblemStalePrincipalRevision)
	case errors.Is(err, authorization.ErrConflict):
		writeProblem(writer, contract.ProblemConflict)
	case errors.Is(err, authorization.ErrStaleCursor):
		writeProblem(writer, contract.ProblemStaleCursor)
	case errors.Is(err, authorization.ErrShuttingDown):
		writeProblem(writer, contract.ProblemShuttingDown)
	default:
		writeProblem(writer, contract.ProblemAuthorizationUnavailable)
	}
}
