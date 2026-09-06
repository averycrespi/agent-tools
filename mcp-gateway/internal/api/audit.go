package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type AuditReader interface {
	List(context.Context, audit.Query) (contract.AuditPage, error)
	Read(context.Context, string, string) (contract.AuditItem, error)
	RecordProblem(context.Context, time.Time, string, string, contract.AuditTarget, contract.ProblemCode) error
}

type auditProblemWriter struct {
	http.ResponseWriter
	handler  *Handler
	request  *http.Request
	admitted bool
}

func (writer *auditProblemWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func (writer *auditProblemWriter) admitProblem(code contract.ProblemCode) contract.ProblemCode {
	if writer.admitted {
		return code
	}
	writer.admitted = true
	return writer.handler.RecordAuthenticatedProblem(writer.request, code)
}

func admitProblem(writer http.ResponseWriter, code contract.ProblemCode) contract.ProblemCode {
	if guarded, ok := writer.(interface {
		admitProblem(contract.ProblemCode) contract.ProblemCode
	}); ok {
		return guarded.admitProblem(code)
	}
	return code
}

func (handler *Handler) RecordAuthenticatedProblem(request *http.Request, code contract.ProblemCode) contract.ProblemCode {
	if _, ok := request.Context().Value(authContextKey{}).(authentication); !ok || handler.audit == nil {
		return code
	}
	category, action, target, ok := auditMutationTarget(request, handler.installationID)
	if !ok {
		return code
	}
	if err := handler.audit.RecordProblem(request.Context(), time.Now(), category, action, target, code); err != nil {
		return contract.ProblemStorageUnavailable
	}
	return code
}

func auditMutationTarget(request *http.Request, installationID string) (string, string, contract.AuditTarget, bool) {
	route, ok := contract.RouteForPath(request.URL.Path)
	if !ok {
		return "", "", contract.AuditTarget{}, false
	}
	type mutation struct{ category, action, targetType, parameter string }
	selected, ok := map[string]mutation{
		"POST /api/v1/admin-sessions":                             {"admin_session", "sign_in", "admin_credential", ""},
		"DELETE /api/v1/admin-sessions/current":                   {"admin_session", "logout", "admin_credential", ""},
		"POST /api/v1/admin-credentials":                          {"admin_credential", "create", "installation", ""},
		"DELETE /api/v1/admin-credentials/{id}":                   {"admin_credential", "revoke", "admin_credential", "{id}"},
		"POST /api/v1/admin-credentials/{id}/rotation-completion": {"admin_credential", "rotate", "admin_credential", "{id}"},
		"POST /api/v1/backups":                                    {"backup", "create", "installation", ""},
		"DELETE /api/v1/backups/{id}":                             {"backup", "delete", "backup", "{id}"},
		"POST /api/v1/servers":                                    {"server", "create", "installation", ""},
		"PATCH /api/v1/servers/{id}":                              {"server", "update", "server", "{id}"},
		"DELETE /api/v1/servers/{id}":                             {"server", "delete", "server", "{id}"},
		"POST /api/v1/servers/{id}/operations":                    {"operation", "request", "server", "{id}"},
		"POST /api/v1/servers/{id}/credential-replacements":       {"server_credential", "replace", "server", "{id}"},
		"POST /api/v1/servers/{id}/auth-flows":                    {"oauth", "create", "server", "{id}"},
		"DELETE /api/v1/servers/{id}/auth-flows/{flow_id}":        {"oauth", "cancel", "auth_flow", "{flow_id}"},
		"POST /api/v1/principals":                                 {"principal", "create", "installation", ""},
		"PATCH /api/v1/principals/{id}":                           {"principal", "update", "principal", "{id}"},
		"POST /api/v1/principals/{id}/credential":                 {"agent_credential", "issue", "principal", "{id}"},
		"DELETE /api/v1/principals/{id}/credential":               {"agent_credential", "revoke", "principal", "{id}"},
		"POST /api/v1/grants":                                     {"grant", "create", "installation", ""},
		"PATCH /api/v1/grants/{id}":                               {"grant", "update", "grant", "{id}"},
		"DELETE /api/v1/grants/{id}":                              {"grant", "delete", "grant", "{id}"},
		"POST /api/v1/grant-requests/{id}/approve":                {"grant_request", "approve", "grant_request", "{id}"},
		"POST /api/v1/grant-requests/{id}/reject":                 {"grant_request", "reject", "grant_request", "{id}"},
	}[request.Method+" "+route.Pattern]
	if !ok {
		return "", "", contract.AuditTarget{}, false
	}
	target := contract.AuditTarget{Type: "installation", ID: installationID}
	if selected.category == "admin_session" {
		if identity, ok := request.Context().Value(authContextKey{}).(authentication); ok {
			target = contract.AuditTarget{Type: "admin_credential", ID: identity.credential.ID}
		}
	} else if selected.parameter != "" {
		members := strings.Split(request.URL.Path, "/")
		for index, segment := range strings.Split(route.Pattern, "/") {
			if segment == selected.parameter && index < len(members) && contract.ValidAuditID(members[index]) {
				target = contract.AuditTarget{Type: selected.targetType, ID: members[index]}
			}
		}
	}
	return selected.category, selected.action, target, true
}

func (handler *Handler) auditCollection(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	query, err := parseAuditQuery(request.URL.RawQuery, false)
	if err != nil {
		writeAuditError(writer, err)
		return
	}
	page, err := handler.audit.List(request.Context(), query)
	if err != nil {
		writeAuditError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (handler *Handler) auditMember(writer http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodGet {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	if !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	query, err := parseAuditQuery(request.URL.RawQuery, true)
	if err != nil {
		writeAuditError(writer, err)
		return
	}
	item, err := handler.audit.Read(request.Context(), id, query.Generation)
	if err != nil {
		writeAuditError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}

func parseAuditQuery(raw string, item bool) (audit.Query, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return audit.Query{}, audit.ErrInvalidInput
	}
	allowed := map[string]bool{"generation": true}
	if !item {
		for _, key := range []string{"cursor", "limit", "actor_type", "credential_id", "category", "action", "target_type", "target_id", "outcome", "correlation_id", "from", "until"} {
			allowed[key] = true
		}
	}
	for key, members := range values {
		if !allowed[key] || len(members) != 1 || members[0] == "" {
			return audit.Query{}, audit.ErrInvalidInput
		}
	}
	query := audit.Query{Limit: contract.AdminListPageDefault, Cursor: values.Get("cursor"), Generation: values.Get("generation"), Filters: contract.AuditFilters{
		ActorType: contract.AuditActorType(values.Get("actor_type")), CredentialID: values.Get("credential_id"), Category: values.Get("category"), Action: values.Get("action"),
		TargetType: values.Get("target_type"), TargetID: values.Get("target_id"), Outcome: values.Get("outcome"), CorrelationID: values.Get("correlation_id"), From: values.Get("from"), Until: values.Get("until"),
	}}
	if value := values.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > contract.AuditPageLimit || strconv.Itoa(limit) != value {
			return audit.Query{}, audit.ErrInvalidInput
		}
		query.Limit = limit
	}
	if len(query.Cursor) > contract.AuditCursorBytes {
		return audit.Query{}, audit.ErrInvalidCursor
	}
	if contract.ValidateAuditFilters(query.Filters) != nil {
		return audit.Query{}, audit.ErrInvalidInput
	}
	return query, nil
}

func writeAuditError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, audit.ErrNotFound):
		writeProblem(writer, contract.ProblemNotFound)
	case errors.Is(err, audit.ErrInvalidInput):
		writeProblem(writer, contract.ProblemMalformedRequest)
	case errors.Is(err, audit.ErrInvalidCursor):
		writeProblem(writer, contract.ProblemInvalidCursor)
	case errors.Is(err, audit.ErrStaleCursor):
		writeProblem(writer, contract.ProblemStaleCursor)
	case errors.Is(err, audit.ErrHistoryReplaced):
		writeProblem(writer, contract.ProblemAuditHistoryReplaced)
	default:
		writeProblem(writer, contract.ProblemStorageUnavailable)
	}
}
