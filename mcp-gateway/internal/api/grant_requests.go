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

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type GrantRequestService interface {
	ListAdmin(context.Context, grantrequests.AdminFilter, *grantrequests.AdminCursor, int) (grantrequests.AdminPage, error)
	GetAdmin(context.Context, string) (contract.GrantRequest, error)
	ApproveAdmin(context.Context, string, string, *string, contract.Policy) (contract.GrantRequest, error)
	RejectAdmin(context.Context, string, string, contract.GrantRequestRejectionReason) (contract.GrantRequest, error)
}

type rawGrantRequestApproval struct {
	Description    json.RawMessage `json:"description"`
	ApprovedPolicy json.RawMessage `json:"approved_policy"`
}

type rawPolicy struct {
	Scope                   json.RawMessage `json:"scope"`
	Target                  json.RawMessage `json:"target"`
	Constraint              json.RawMessage `json:"constraint"`
	DurationSeconds         json.RawMessage `json:"duration_seconds"`
	FutureToolsAcknowledged json.RawMessage `json:"future_tools_acknowledged"`
}

func (handler *Handler) grantRequestsCollection(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	limit, filter, cursor, problem := parseGrantRequestQuery(request.URL.RawQuery)
	if problem != "" {
		writeProblem(writer, problem)
		return
	}
	page, err := handler.grantRequests.ListAdmin(request.Context(), filter, cursor, limit)
	if err != nil {
		writeGrantRequestError(writer, err)
		return
	}
	var next *string
	if page.Next != nil {
		value := encodeGrantRequestCursor(*page.Next)
		next = &value
	}
	writeJSON(writer, http.StatusOK, contract.Collection[contract.GrantRequestSummary]{Items: page.Items, NextCursor: next})
}

func (handler *Handler) grantRequestMember(writer http.ResponseWriter, request *http.Request, requestID string, action string) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	switch action {
	case "":
		if request.Method != http.MethodGet {
			writeProblem(writer, contract.ProblemNotFound)
			return
		}
		if !bodyless(request) {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
		item, err := handler.grantRequests.GetAdmin(request.Context(), requestID)
		if err != nil {
			writeGrantRequestError(writer, err)
			return
		}
		writeGrantRequest(writer, item)
	case "approve":
		if request.Method != http.MethodPost {
			writeProblem(writer, contract.ProblemNotFound)
			return
		}
		revision, ok := grantRequestPrecondition(writer, request, requestID)
		if !ok {
			return
		}
		var raw rawGrantRequestApproval
		if !decodeStrictBody(writer, request, &raw) {
			return
		}
		var description *string
		policy, ok := decodeApprovalPolicy(raw.ApprovedPolicy)
		if !decodeNullableGrantMember(raw.Description, &description) || !ok {
			writeProblem(writer, contract.ProblemInvalidGrantRequest)
			return
		}
		item, err := handler.grantRequests.ApproveAdmin(request.Context(), requestID, revision, description, policy)
		if err != nil {
			writeGrantRequestError(writer, err)
			return
		}
		writeGrantRequest(writer, item)
	case "reject":
		if request.Method != http.MethodPost {
			writeProblem(writer, contract.ProblemNotFound)
			return
		}
		revision, ok := grantRequestPrecondition(writer, request, requestID)
		if !ok {
			return
		}
		var rejection contract.GrantRequestRejection
		if !decodeStrictBody(writer, request, &rejection) {
			return
		}
		if _, err := contract.ParseGrantRequestRejectionReason(string(rejection.Reason)); err != nil {
			writeProblem(writer, contract.ProblemInvalidGrantRequest)
			return
		}
		item, err := handler.grantRequests.RejectAdmin(request.Context(), requestID, revision, rejection.Reason)
		if err != nil {
			writeGrantRequestError(writer, err)
			return
		}
		writeGrantRequest(writer, item)
	default:
		writeProblem(writer, contract.ProblemNotFound)
	}
}

func writeGrantRequest(writer http.ResponseWriter, request contract.GrantRequest) {
	writer.Header().Set("ETag", contract.GrantRequestETag(request.ID, request.Revision))
	writeJSON(writer, http.StatusOK, request)
}

func parseGrantRequestQuery(rawQuery string) (int, grantrequests.AdminFilter, *grantrequests.AdminCursor, contract.ProblemCode) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, grantrequests.AdminFilter{}, nil, contract.ProblemMalformedRequest
	}
	for key, values := range query {
		if (key != "cursor" && key != "limit" && key != "principal_id" && key != "state") || len(values) != 1 || values[0] == "" || values[0] == "null" {
			return 0, grantrequests.AdminFilter{}, nil, contract.ProblemMalformedRequest
		}
	}
	limit := contract.S5ListPageDefault
	if values, ok := query["limit"]; ok {
		value, err := strconv.Atoi(values[0])
		if err != nil || value < 1 || value > limitValue("admin_list_page") {
			return 0, grantrequests.AdminFilter{}, nil, contract.ProblemMalformedRequest
		}
		limit = value
	}
	filter := grantrequests.AdminFilter{}
	if values, ok := query["principal_id"]; ok {
		filter.PrincipalID = values[0]
	}
	if values, ok := query["state"]; ok {
		state, err := contract.ParseGrantRequestState(values[0])
		if err != nil {
			return 0, grantrequests.AdminFilter{}, nil, contract.ProblemMalformedRequest
		}
		filter.State = &state
	}
	var cursor *grantrequests.AdminCursor
	if values, ok := query["cursor"]; ok {
		if len(values[0]) > limitValue("cursor_bytes") {
			return 0, grantrequests.AdminFilter{}, nil, contract.ProblemInvalidCursor
		}
		contents, err := base64.RawURLEncoding.DecodeString(values[0])
		var decoded grantrequests.AdminCursor
		if err != nil || strictjson.DecodeReader(bytes.NewReader(contents), &decoded, strictjson.Options{
			MaxBytes: int64(limitValue("cursor_bytes")), MaxDepth: limitValue("json_depth"), RejectUnknownMembers: true,
		}) != nil {
			return 0, grantrequests.AdminFilter{}, nil, contract.ProblemInvalidCursor
		}
		cursor = &decoded
	}
	return limit, filter, cursor, ""
}

func encodeGrantRequestCursor(cursor grantrequests.AdminCursor) string {
	contents, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(contents)
}

func decodeApprovalPolicy(contents json.RawMessage) (contract.Policy, bool) {
	if contents == nil || bytes.Equal(contents, []byte("null")) {
		return contract.Policy{}, false
	}
	var raw rawPolicy
	if strictjson.Decode(contents, &raw, strictjson.Options{
		MaxBytes: int64(limitValue("api_json_body_bytes")), MaxDepth: limitValue("json_depth"), RejectUnknownMembers: true,
	}) != nil {
		return contract.Policy{}, false
	}
	var policy contract.Policy
	if !decodeRequiredGrantMember(raw.Scope, &policy.Scope) || !decodeRequiredGrantMember(raw.Target, &policy.Target) ||
		!decodeRequiredGrantMember(raw.FutureToolsAcknowledged, &policy.FutureToolsAcknowledged) || raw.Constraint == nil || raw.DurationSeconds == nil {
		return contract.Policy{}, false
	}
	if !bytes.Equal(raw.Constraint, []byte("null")) {
		value := json.RawMessage(append([]byte(nil), raw.Constraint...))
		policy.Constraint = &value
	}
	if !bytes.Equal(raw.DurationSeconds, []byte("null")) {
		var value string
		if json.Unmarshal(raw.DurationSeconds, &value) != nil {
			return contract.Policy{}, false
		}
		policy.DurationSeconds = &value
	}
	return policy, true
}

func grantRequestPrecondition(writer http.ResponseWriter, request *http.Request, requestID string) (string, bool) {
	values := request.Header.Values("If-Match")
	if len(values) == 0 {
		writeProblem(writer, contract.ProblemGrantRequestPreconditionRequired)
		return "", false
	}
	if len(values) != 1 {
		writeProblem(writer, contract.ProblemStaleGrantRequestRevision)
		return "", false
	}
	prefix := `"grant-request-` + requestID + `-`
	value := values[0]
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		writeProblem(writer, contract.ProblemStaleGrantRequestRevision)
		return "", false
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
	parsed, err := strconv.ParseUint(revision, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != revision {
		writeProblem(writer, contract.ProblemStaleGrantRequestRevision)
		return "", false
	}
	return revision, true
}

func writeGrantRequestError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, grantrequests.ErrNotFound):
		writeProblem(writer, contract.ProblemNotFound)
	case errors.Is(err, grantrequests.ErrInvalidInput):
		writeProblem(writer, contract.ProblemInvalidGrantRequest)
	case errors.Is(err, grantrequests.ErrResourceLimit):
		writeProblem(writer, contract.ProblemResourceLimit)
	case errors.Is(err, grantrequests.ErrStaleRevision):
		writeProblem(writer, contract.ProblemStaleGrantRequestRevision)
	case errors.Is(err, grantrequests.ErrConflict):
		writeProblem(writer, contract.ProblemGrantRequestConflict)
	case errors.Is(err, grantrequests.ErrStaleCursor):
		writeProblem(writer, contract.ProblemStaleCursor)
	default:
		writeProblem(writer, contract.ProblemAuthorizationUnavailable)
	}
}
