package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/configexport"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type ConfigurationExporter interface {
	ExportBundle(context.Context) ([]byte, error)
	ExportResource(context.Context, string, string) ([]byte, error)
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
	var body []byte
	var err error
	if r.URL.Path == contract.ConfigExportPath {
		body, err = h.configuration.ExportBundle(r.Context())
	} else {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, contract.ConfigResourcePath), "/")
		if len(parts) != 2 || !contract.ConfigResourceKind(parts[0]) || !contract.ValidResourceKey(parts[1]) || parts[0] == "mcp_server" && (len(parts[1]) > 32 || parts[1] == contract.SyntheticServerNamespace) {
			writeProblem(w, contract.ProblemMalformedRequest)
			return
		}
		body, err = h.configuration.ExportResource(r.Context(), parts[0], parts[1])
	}
	if errors.Is(err, configexport.ErrConfigurationNotFound) {
		writeProblem(w, contract.ProblemNotFound)
		return
	}
	if err != nil {
		writeProblem(w, contract.ProblemStorageUnavailable)
		return
	}
	// The exporter validates and serializes the complete snapshot before headers.
	// Content-Length makes a failed/short delivery detectable by the finite client.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if n, err := w.Write(body); err != nil || n != len(body) { //nolint:gosec // ExportResource/ExportBundle validate and JSON-serialize configuration; this response is application/json, never HTML.
		panic(http.ErrAbortHandler)
	}
}
