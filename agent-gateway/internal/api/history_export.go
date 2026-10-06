package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
)

type HistoryExporter interface {
	ExportHistoryThrough(context.Context, int64, int64, int) (contract.HistoryExport, error)
}

func (handler *Handler) historyExport(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || !bodyless(request) {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeProblem(writer, contract.ProblemMalformedRequest)
		return
	}
	for key, values := range query {
		if (key != "after_sequence" && key != "through_sequence" && key != "limit") || len(values) != 1 || values[0] == "" {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
	}
	after := int64(0)
	limit := contract.HistoryExportMaxRecords
	if text := query.Get("after_sequence"); text != "" {
		after, err = strconv.ParseInt(text, 10, 64)
		if err != nil || after < 0 || strconv.FormatInt(after, 10) != text {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
	}
	if text := query.Get("limit"); text != "" {
		limit, err = strconv.Atoi(text)
		if err != nil || limit < 1 || limit > contract.HistoryExportMaxRecords || strconv.Itoa(limit) != text {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
	}
	through := int64(math.MaxInt64)
	if text := query.Get("through_sequence"); text != "" {
		through, err = strconv.ParseInt(text, 10, 64)
		if err != nil || through < after || strconv.FormatInt(through, 10) != text {
			writeProblem(writer, contract.ProblemMalformedRequest)
			return
		}
	}
	value, err := handler.history.ExportHistoryThrough(request.Context(), after, through, limit)
	if err != nil {
		switch {
		case errors.Is(err, invocation.ErrInvalidInput):
			writeProblem(writer, contract.ProblemMalformedRequest)
		case errors.Is(err, invocation.ErrTrafficCapacity), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			writeProblem(writer, contract.ProblemHistoryBusy)
		default:
			writeProblem(writer, contract.ProblemHistoryUnavailable)
		}
		return
	}
	writeJSON(writer, http.StatusOK, value)
}
