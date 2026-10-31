package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type ConfigurationExporter interface {
	ExportConfiguration(context.Context) ([]byte, error)
}

func (h *Handler) configurationExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !bodyless(r) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	if h.configuration == nil {
		writeProblem(w, contract.ProblemStorageUnavailable)
		return
	}
	body, err := h.configuration.ExportConfiguration(r.Context())
	if err != nil {
		writeProblem(w, contract.ProblemStorageUnavailable)
		return
	}
	// The exporter validates and serializes the complete snapshot before headers.
	// Content-Length makes a failed/short delivery detectable by the finite client.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if n, err := w.Write(body); err != nil || n != len(body) {
		panic(http.ErrAbortHandler)
	}
}
