package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type HTTPTrafficReader interface {
	ListHTTP(context.Context, contract.HTTPTrafficQuery) (contract.HTTPTrafficPage, error)
	GetHTTP(context.Context, string) (contract.HTTPTrafficRecord, error)
}

func (h *Handler) httpTrafficCollection(w http.ResponseWriter, r *http.Request) {
	if !bodyless(r) {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	allowed := map[string]bool{"cursor": true, "limit": true, "principal_id": true, "destination": true, "type": true, "decision": true, "outcome": true}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
	}
	q := contract.HTTPTrafficQuery{Limit: contract.AdminListPageDefault, Cursor: query.Get("cursor"), Filters: contract.HTTPTrafficFilters{PrincipalID: query.Get("principal_id"), Destination: query.Get("destination"), Type: query.Get("type"), Decision: query.Get("decision"), Outcome: query.Get("outcome")}}
	if raw := query.Get("limit"); raw != "" {
		q.Limit, err = strconv.Atoi(raw)
		if err != nil || q.Limit < 1 || q.Limit > limitValue("admin_list_page") || strconv.Itoa(q.Limit) != raw {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
	}
	page, err := h.httpTraffic.ListHTTP(r.Context(), q)
	if err != nil {
		writeInvocationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) httpTrafficMember(w http.ResponseWriter, r *http.Request, id string) {
	if !bodyless(r) || r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	item, err := h.httpTraffic.GetHTTP(r.Context(), id)
	if err != nil {
		writeInvocationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
