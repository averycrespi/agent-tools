package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type ProtocolActivityReader interface {
	ProtocolActivity(context.Context, string) (contract.ProtocolActivity, error)
}

func (h *Handler) protocolActivitySummary(w http.ResponseWriter, r *http.Request) {
	if !bodyless(r) || r.URL.ForceQuery {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	window := "1h"
	for key, values := range query {
		if key != "window" || len(values) != 1 || contract.ProtocolActivityWindow(values[0]) == 0 {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		window = values[0]
	}
	if h.protocolActivity == nil {
		writeProblem(w, contract.ProblemStorageUnavailable)
		return
	}
	result, err := h.protocolActivity.ProtocolActivity(r.Context(), window)
	if err != nil {
		writeInvocationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
