package dashboard

import (
	"fmt"
	"net/http"
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
