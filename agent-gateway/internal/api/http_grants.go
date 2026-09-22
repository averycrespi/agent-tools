package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

type HTTPPolicyService interface {
	GetHTTPGrant(context.Context, string) (contract.HTTPGrant, error)
	PutHTTPGrant(context.Context, string, string, authorization.HTTPGrantInput) (contract.HTTPGrant, error)
	DeleteHTTPGrant(context.Context, string, string) error
	QueryHTTPGrants(context.Context, authorization.CollectionQuery, *authorization.SnapshotCursor, int) (authorization.HTTPGrantPage, error)
	GetHTTPDefault(context.Context, string) (contract.PrincipalHTTPDefault, error)
	SetHTTPDefault(context.Context, string, string, contract.HTTPDefault) (contract.PrincipalHTTPDefault, error)
	PreviewHTTPAccess(context.Context, authorization.HTTPAccessInput) (contract.HTTPAccessPreview, error)
}

type rawHTTPGrant struct {
	PrincipalID json.RawMessage `json:"principal_id"`
	Description json.RawMessage `json:"description"`
	Policy      json.RawMessage `json:"policy"`
	ExpiresAt   json.RawMessage `json:"expires_at"`
}

func httpPolicyETag(kind, id, revision string) string {
	return `"http-` + kind + `-` + id + `-` + revision + `"`
}
func httpPolicyPrecondition(w http.ResponseWriter, r *http.Request, kind, id string) (string, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		writeProblem(w, contract.ProblemGrantPreconditionRequired)
		return "", false
	}
	prefix := `"http-` + kind + `-` + id + `-`
	if len(values) != 1 || !strings.HasPrefix(values[0], prefix) || !strings.HasSuffix(values[0], `"`) {
		writeProblem(w, contract.ProblemStaleGrantRevision)
		return "", false
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(values[0], prefix), `"`)
	n, err := strconv.ParseUint(revision, 10, 63)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != revision {
		writeProblem(w, contract.ProblemStaleGrantRevision)
		return "", false
	}
	return revision, true
}

func (h *Handler) httpGrants(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet && id == "" {
		h.listHTTPGrants(w, r)
		return
	}
	if r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	if r.Method == http.MethodGet {
		if !bodyless(r) {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		g, err := h.httpPolicies.GetHTTPGrant(r.Context(), id)
		if err != nil {
			writeGrantError(w, err)
			return
		}
		w.Header().Set("ETag", httpPolicyETag("grant", g.ID, g.Revision))
		writeJSON(w, http.StatusOK, g)
		return
	}
	revision := ""
	if id != "" {
		var ok bool
		revision, ok = httpPolicyPrecondition(w, r, "grant", id)
		if !ok {
			return
		}
	}
	if r.Method == http.MethodDelete {
		if !bodyless(r) {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		if err := h.httpPolicies.DeleteHTTPGrant(r.Context(), id, revision); err != nil {
			writeGrantError(w, err)
			return
		}
		h.emitHTTPPolicy()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var raw rawHTTPGrant
	if !decodeStrictBody(w, r, &raw) {
		return
	}
	var in authorization.HTTPGrantInput
	var expiry *string
	if !decodeRequiredGrantMember(raw.PrincipalID, &in.PrincipalID) || !decodeNullableGrantMember(raw.Description, &in.Description) || !decodeNullableGrantMember(raw.ExpiresAt, &expiry) || raw.Policy == nil {
		writeProblem(w, contract.ProblemInvalidGrant)
		return
	}
	if expiry != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *expiry)
		if err != nil || parsed.UTC().Format(time.RFC3339Nano) != *expiry {
			writeProblem(w, contract.ProblemInvalidGrant)
			return
		}
		in.ExpiresAt = &parsed
	}
	in.Policy = raw.Policy
	g, err := h.httpPolicies.PutHTTPGrant(r.Context(), id, revision, in)
	if err != nil {
		writeGrantError(w, err)
		return
	}
	h.emitHTTPPolicy()
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	w.Header().Set("ETag", httpPolicyETag("grant", g.ID, g.Revision))
	writeJSON(w, status, g)
}

func (h *Handler) listHTTPGrants(w http.ResponseWriter, r *http.Request) {
	if !bodyless(r) {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	var query authorization.CollectionQuery
	fields := map[string]*string{"identity": &query.Identity, "principal": &query.Principal, "target": &query.Target, "type": &query.Effect, "state": &query.State, "sort": &query.Sort, "direction": &query.Direction, "principal_id": &query.PrincipalID}
	for key, members := range values {
		if key == "limit" || key == "cursor" {
			continue
		}
		destination, ok := fields[key]
		if !ok || len(members) != 1 || members[0] == "" {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		*destination = members[0]
		values.Del(key)
	}
	limit, _, cursor, problem := parseGrantQuery(values.Encode())
	if problem != "" {
		writeProblem(w, problem)
		return
	}
	page, err := h.httpPolicies.QueryHTTPGrants(r.Context(), query, cursor, limit)
	if err != nil {
		writeGrantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contract.QueryCollection[contract.HTTPGrantTableItem]{Collection: contract.Collection[contract.HTTPGrantTableItem]{Items: page.Items, NextCursor: nextAuthorizationCursor(page.Next)}, CollectionRange: page.CollectionRange})
}

func (h *Handler) httpDefault(w http.ResponseWriter, r *http.Request, id string) {
	if r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	var result contract.PrincipalHTTPDefault
	var err error
	if r.Method == http.MethodGet {
		if !bodyless(r) {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		result, err = h.httpPolicies.GetHTTPDefault(r.Context(), id)
	} else {
		revision, ok := httpPolicyPrecondition(w, r, "default", id)
		if !ok {
			return
		}
		var in struct {
			Default *contract.HTTPDefault `json:"default"`
		}
		if !decodeStrictBody(w, r, &in) {
			return
		}
		if in.Default == nil {
			writeProblem(w, contract.ProblemInvalidGrant)
			return
		}
		result, err = h.httpPolicies.SetHTTPDefault(r.Context(), id, revision, *in.Default)
		if err == nil {
			h.emitHTTPPolicy()
		}
	}
	if err != nil {
		writeGrantError(w, err)
		return
	}
	w.Header().Set("ETag", httpPolicyETag("default", result.PrincipalID, result.Revision))
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) previewHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	var raw struct {
		PrincipalID json.RawMessage `json:"principal_id"`
		URL         json.RawMessage `json:"url"`
		Method      json.RawMessage `json:"method"`
		Connect     json.RawMessage `json:"connect"`
	}
	if !decodeStrictBody(w, r, &raw) {
		return
	}
	var in authorization.HTTPAccessInput
	valid := decodeRequiredGrantMember(raw.PrincipalID, &in.PrincipalID)
	if raw.Connect != nil {
		var destination struct {
			Host *string `json:"host"`
			Port *uint16 `json:"port"`
		}
		valid = valid && raw.URL == nil && raw.Method == nil && strictjson.Decode(raw.Connect, &destination, strictjson.Options{MaxBytes: 1024, MaxDepth: 2, RejectUnknownMembers: true}) == nil && destination.Host != nil && destination.Port != nil
		if valid {
			in.Connect = &contract.HTTPDestinationSelector{Host: *destination.Host, Port: *destination.Port}
		}
	} else {
		valid = valid && decodeRequiredGrantMember(raw.URL, &in.URL) && decodeRequiredGrantMember(raw.Method, &in.Method)
	}
	if !valid {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	result, err := h.httpPolicies.PreviewHTTPAccess(r.Context(), in)
	if err != nil {
		writeGrantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) emitHTTPPolicy() {
	h.emit(contract.Invalidation{Kind: contract.InvalidationAuthorization})
	h.emit(contract.Invalidation{Kind: contract.InvalidationHTTPCredentials})
}
