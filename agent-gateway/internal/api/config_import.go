package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configdefinition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configimport"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

type ConfigurationImporter interface {
	Preview(context.Context, []byte) (contract.ConfigPreview, error)
	Import(context.Context, []byte) (contract.ConfigImportResult, error)
}

func (h *Handler) configurationInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	if h.configurationImport == nil {
		writeProblem(w, contract.ProblemStorageUnavailable)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, contract.ConfigExportMaxBytes+1))
	if err != nil || len(raw) > contract.ConfigExportMaxBytes {
		writeProblem(w, contract.ProblemMalformedRequest)
		return
	}
	if r.URL.Path == contract.ConfigPreviewPath {
		result, err := h.configurationImport.Preview(r.Context(), raw)
		if err != nil {
			configurationProblem(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	result, err := h.configurationImport.Import(r.Context(), raw)
	if err != nil {
		configurationProblem(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func configurationProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, configdefinition.ErrInvalid):
		writeProblem(w, contract.ProblemMalformedRequest)
	case errors.Is(err, configimport.ErrConflict), errors.Is(err, resourcekeys.ErrConflict), errors.Is(err, authorization.ErrConflict):
		writeProblem(w, contract.ProblemConflict)
	case errors.Is(err, storage.ErrMutationBusy), errors.Is(err, authorization.ErrResourceLimit):
		writeProblem(w, contract.ProblemResourceLimit)
	default:
		writeProblem(w, contract.ProblemStorageUnavailable)
	}
}
