package dashboard

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
)

func (h *Handler) serveOverview(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	query, err := parseOverviewQuery(r, h.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	overview, err := h.reader.Overview(ctx, query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview query failed")
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (h *Handler) serveSessionStream(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/stream")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	page, err := h.reader.SessionStream(ctx, id, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, store.ErrAmbiguousSession), errors.Is(err, store.ErrInvalidStreamCursor):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "session stream query failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) serveToolOutcomes(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/tools")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	report, err := h.reader.ToolOutcomeReport(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, store.ErrAmbiguousSession):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "tool outcome query failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (h *Handler) serveSessionDiagnostics(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/diagnostics")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	diagnostics, err := h.reader.SessionDiagnostics(ctx, id, h.detectorNames)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, store.ErrAmbiguousSession):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "diagnostics query failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, diagnostics)
}

func (h *Handler) serveTodoDiagnostics(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/todo")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	snapshotOffset, err := queryInteger(r, "snapshot_offset", 0, 0, 1_000_000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshotLimit, err := queryInteger(r, "snapshot_limit", 50, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	itemOffset, err := queryInteger(r, "item_offset", 0, 0, 1_000_000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	itemLimit, err := queryInteger(r, "item_limit", 20, 1, 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	diagnostics, err := h.reader.TodoDiagnosticsPage(ctx, id, store.TodoQuery{SnapshotOffset: snapshotOffset, SnapshotLimit: snapshotLimit, ItemOffset: itemOffset, ItemLimit: itemLimit})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, store.ErrAmbiguousSession):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "todo query failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, diagnostics)
}

func queryInteger(r *http.Request, name string, fallback, minimum, maximum int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return parsed, nil
}

func parseOverviewQuery(r *http.Request, now time.Time) (store.OverviewQuery, error) {
	timezone := r.URL.Query().Get("timezone")
	if timezone == "" {
		timezone = "UTC"
	}
	if timezone == "Local" {
		return store.OverviewQuery{}, fmt.Errorf("timezone must be a valid IANA name")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return store.OverviewQuery{}, fmt.Errorf("timezone must be a valid IANA name")
	}
	localNow := now.In(location)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location)
	var from, to time.Time
	fromText, toText := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if fromText != "" || toText != "" {
		if fromText == "" || toText == "" {
			return store.OverviewQuery{}, fmt.Errorf("from and to dates must be provided together")
		}
		from, err = time.ParseInLocation("2006-01-02", fromText, location)
		if err != nil {
			return store.OverviewQuery{}, fmt.Errorf("from must be YYYY-MM-DD")
		}
		to, err = time.ParseInLocation("2006-01-02", toText, location)
		if err != nil {
			return store.OverviewQuery{}, fmt.Errorf("to must be YYYY-MM-DD")
		}
	} else {
		days := 30
		switch value := r.URL.Query().Get("range"); value {
		case "", "30d":
		case "7d":
			days = 7
		case "90d":
			days = 90
		default:
			return store.OverviewQuery{}, fmt.Errorf("range must be 7d, 30d, or 90d")
		}
		from = today.AddDate(0, 0, -(days - 1))
		to = today.AddDate(0, 0, 1)
	}
	unit := store.BucketUnit(r.URL.Query().Get("bucket"))
	if unit == "" || unit == "auto" {
		switch {
		case !to.After(from.AddDate(0, 0, 90)):
			unit = store.BucketDay
		case !to.After(from.AddDate(0, 18, 0)):
			unit = store.BucketWeek
		default:
			unit = store.BucketMonth
		}
	}
	buckets, err := store.CalendarBuckets(from, to, localNow, location, unit)
	if err != nil {
		return store.OverviewQuery{}, err
	}
	return store.OverviewQuery{Timezone: timezone, Unit: unit, Buckets: buckets}, nil
}
