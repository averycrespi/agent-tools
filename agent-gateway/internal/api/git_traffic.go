package api

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type GitTrafficReader interface {
	ListGit(context.Context, contract.GitTrafficQuery) (contract.GitTrafficPage, error)
	GetGit(context.Context, string) (contract.GitTrafficRecord, error)
}

func (h *Handler) gitTrafficCollection(w http.ResponseWriter, r *http.Request) {
	if !bodyless(r) {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	for key, values := range query {
		if !slices.Contains([]string{"from", "until", "cursor", "limit", "operation", "repository", "admission", "transport", "report", "search_locale"}, key) || len(values) != 1 || values[0] == "" {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
	}
	q := contract.GitTrafficQuery{Limit: contract.AdminListPageDefault, Cursor: query.Get("cursor"), Filters: contract.GitTrafficFilters{
		From: query.Get("from"), Until: query.Get("until"),
		Operation: query.Get("operation"), Repository: query.Get("repository"), Admission: query.Get("admission"), Transport: query.Get("transport"), Report: query.Get("report"), SearchLocale: query.Get("search_locale"),
	}}
	if raw := query.Get("limit"); raw != "" {
		q.Limit, err = strconv.Atoi(raw)
		if err != nil || q.Limit < 1 || q.Limit > limitValue("admin_list_page") || strconv.Itoa(q.Limit) != raw {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
	}
	if !contract.ValidHistoryRange(q.Filters.From, q.Filters.Until) {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	page, err := h.gitTraffic.ListGit(r.Context(), q)
	if err != nil {
		writeInvocationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func (h *Handler) gitTrafficMember(w http.ResponseWriter, r *http.Request, id string) {
	if !bodyless(r) || r.URL.RawQuery != "" {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	item, err := h.gitTraffic.GetGit(r.Context(), id)
	if err != nil {
		writeInvocationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
