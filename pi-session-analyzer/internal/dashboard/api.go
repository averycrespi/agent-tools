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
	overview, status, err := h.overviewForRequest(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (h *Handler) serveOverviewSignals(w http.ResponseWriter, r *http.Request) {
	overview, status, err := h.overviewForRequest(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		BucketSignals store.OverviewBucketSignals  `json:"bucket_signals"`
		StopReasons   []store.CategoryBucketSeries `json:"stop_reasons"`
	}{overview.BucketSignals, overview.StopReasons})
}

func (h *Handler) overviewForRequest(r *http.Request) (store.Overview, int, error) {
	if h.reader == nil {
		return store.Overview{}, http.StatusServiceUnavailable, fmt.Errorf("database is unavailable")
	}
	allowed := map[string]bool{"timezone": true, "range": true, "bucket": true, "from": true, "to": true}
	for name := range r.URL.Query() {
		if !allowed[name] {
			return store.Overview{}, http.StatusBadRequest, fmt.Errorf("unsupported overview parameter")
		}
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	var bounds []store.CanonicalTimeRange
	if r.URL.Query().Get("range") == "all" && r.URL.Query().Get("from") == "" && r.URL.Query().Get("to") == "" {
		value, err := h.reader.CanonicalTimeBounds(ctx)
		if err != nil {
			return store.Overview{}, http.StatusInternalServerError, fmt.Errorf("overview bounds query failed")
		}
		bounds = append(bounds, value)
	}
	query, err := parseOverviewQuery(r, h.now(), bounds...)
	if err != nil {
		return store.Overview{}, http.StatusBadRequest, err
	}
	query.DetectorNames = h.detectorNames
	overview, err := h.reader.Overview(ctx, query)
	if err != nil {
		return store.Overview{}, http.StatusInternalServerError, fmt.Errorf("overview query failed")
	}
	return overview, http.StatusOK, nil
}

func (h *Handler) serveSessionMatrix(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	allowed := map[string]bool{"from": true, "to": true, "untimed": true, "cwd": true, "limit": true, "cursor": true, "direction": true}
	for name := range r.URL.Query() {
		if !allowed[name] {
			writeError(w, http.StatusBadRequest, "unsupported matrix parameter")
			return
		}
	}
	limit, err := queryInteger(r, "limit", store.MaxMatrixPageSize, 1, store.MaxMatrixPageSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	untimed := false
	if value := r.URL.Query().Get("untimed"); value != "" {
		if value != "true" && value != "false" {
			writeError(w, http.StatusBadRequest, "untimed must be true or false")
			return
		}
		untimed = value == "true"
	}
	from, to := h.now().AddDate(0, 0, -30).Unix(), h.now().Unix()
	if value := r.URL.Query().Get("from"); value != "" {
		from, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be a Unix timestamp")
			return
		}
	}
	if value := r.URL.Query().Get("to"); value != "" {
		to, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be a Unix timestamp")
			return
		}
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	page, err := h.reader.SessionMatrix(ctx, store.MatrixQuery{FromUnix: from, ToUnix: to, Untimed: untimed, CWD: r.URL.Query().Get("cwd"), Limit: limit, Cursor: r.URL.Query().Get("cursor"), Direction: r.URL.Query().Get("direction")}, h.detectorNames)
	if err != nil {
		if errors.Is(err, store.ErrInvalidMatrixQuery) {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "session matrix query failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) serveSessionHeader(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	if len(r.URL.Query()) != 0 {
		writeError(w, http.StatusBadRequest, "session header does not accept parameters")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	header, err := h.reader.SessionHeader(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, store.ErrAmbiguousSession):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "session header query failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, header)
}

func (h *Handler) serveTokenSequence(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	for name := range r.URL.Query() {
		if name != "limit" && name != "cursor" {
			writeError(w, http.StatusBadRequest, "unsupported token parameter")
			return
		}
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/tokens")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	limit, err := queryInteger(r, "limit", store.MaxTokenSequencePageSize, 1, store.MaxTokenSequencePageSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	page, err := h.reader.MessageTokenSequence(ctx, id, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, store.ErrAmbiguousSession), errors.Is(err, store.ErrInvalidStreamCursor):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "token sequence query failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) serveSessionStream(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	for name := range r.URL.Query() {
		if name != "limit" && name != "cursor" && name != "anchor_line" && name != "anchor_id" {
			writeError(w, http.StatusBadRequest, "unsupported stream parameter")
			return
		}
	}
	anchorLine, anchorErr := queryInteger(r, "anchor_line", 0, 0, 10_000_000)
	anchorID := r.URL.Query().Get("anchor_id")
	if anchorErr != nil || len(anchorID) > 256 || (anchorLine > 0 && r.URL.Query().Get("cursor") != "") || (anchorID != "" && anchorLine == 0) {
		writeError(w, http.StatusBadRequest, "bounded anchor_line/anchor_id cannot be combined with cursor")
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
	page, err := h.reader.SessionStreamFromEvidence(ctx, id, r.URL.Query().Get("cursor"), limit, anchorLine, anchorID)
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
	if len(r.URL.Query()) != 0 {
		writeError(w, http.StatusBadRequest, "tool outcomes do not accept parameters")
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
	if len(r.URL.Query()) != 0 {
		writeError(w, http.StatusBadRequest, "diagnostics do not accept parameters")
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

func (h *Handler) serveEntryDetail(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	for name := range r.URL.Query() {
		if name != "kind" && name != "id" {
			writeError(w, http.StatusBadRequest, "unsupported detail parameter")
			return
		}
	}
	entryID, kind := r.URL.Query().Get("id"), r.URL.Query().Get("kind")
	if entryID == "" || len(entryID) > 256 || kind == "" {
		writeError(w, http.StatusBadRequest, "detail kind and bounded id are required")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/detail")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	detail, err := h.reader.SessionEntryDetail(ctx, id, kind, entryID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidEntryKind):
			writeError(w, http.StatusBadRequest, "invalid detail kind")
		case errors.Is(err, store.ErrSessionNotFound), errors.Is(err, store.ErrEntryNotFound):
			writeError(w, http.StatusNotFound, "entry not found")
		case errors.Is(err, store.ErrAmbiguousSession):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "entry detail query failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) serveGoalDiagnostics(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database is unavailable")
		return
	}
	for name := range r.URL.Query() {
		if name != "offset" && name != "limit" {
			writeError(w, http.StatusBadRequest, "unsupported goal parameter")
			return
		}
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/goal")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	offset, err := queryInteger(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := queryInteger(r, "limit", 50, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := robound.WithTimeout(r.Context())
	defer cancel()
	diagnostics, err := h.reader.GoalDiagnosticsPage(ctx, id, offset, limit)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, store.ErrAmbiguousSession):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "goal query failed")
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
	allowed := map[string]bool{"snapshot_offset": true, "snapshot_limit": true, "item_offset": true, "item_limit": true}
	for name := range r.URL.Query() {
		if !allowed[name] {
			writeError(w, http.StatusBadRequest, "unsupported todo parameter")
			return
		}
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

func parseOverviewQuery(r *http.Request, now time.Time, allBounds ...store.CanonicalTimeRange) (store.OverviewQuery, error) {
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
		case "all":
			if len(allBounds) != 1 {
				return store.OverviewQuery{}, fmt.Errorf("all-history bounds are unavailable")
			}
			if allBounds[0].Minimum == nil {
				from, to = today, today.AddDate(0, 0, 1)
			} else {
				minimum := time.Unix(*allBounds[0].Minimum, 0).In(location)
				from = time.Date(minimum.Year(), minimum.Month(), minimum.Day(), 0, 0, 0, 0, location)
				to = today.AddDate(0, 0, 1)
				maximum := time.Unix(*allBounds[0].Maximum, 0).In(location)
				maximumEnd := time.Date(maximum.Year(), maximum.Month(), maximum.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1)
				if maximumEnd.After(to) {
					to = maximumEnd
				}
			}
		default:
			return store.OverviewQuery{}, fmt.Errorf("range must be 7d, 30d, 90d, or all")
		}
		if from.IsZero() {
			from = today.AddDate(0, 0, -(days - 1))
			to = today.AddDate(0, 0, 1)
		}
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
