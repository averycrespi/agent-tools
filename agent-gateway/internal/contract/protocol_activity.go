package contract

import "time"

// ProtocolActivity is retained admission-time history, not process counters or
// proof of complete recording. Counts are absent when aggregation is unavailable.
type ProtocolActivity struct {
	Window   string                  `json:"window"`
	From     string                  `json:"from"`
	Until    string                  `json:"until"`
	Coverage string                  `json:"coverage"`
	Counts   *ProtocolActivityCounts `json:"counts"`
}
type ProtocolActivityCounts struct {
	HTTP ProtocolOutcomes `json:"http"`
	Git  ProtocolOutcomes `json:"git"`
	MCP  ProtocolOutcomes `json:"mcp"`
}
type ProtocolOutcomes struct {
	Total           int64 `json:"total"`
	Success         int64 `json:"success"`
	Other           int64 `json:"other"`
	ReportedSuccess int64 `json:"reported_success"`
	Failed          int64 `json:"failed"`
	Denied          int64 `json:"denied"`
	Rejected        int64 `json:"rejected"`
	Unknown         int64 `json:"unknown"`
	Incomplete      int64 `json:"incomplete"`
	ReportedPartial int64 `json:"reported_partial"`
}

func ProtocolActivityWindow(window string) time.Duration {
	switch window {
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "24h":
		return 24 * time.Hour
	default:
		return 0
	}
}

// History ranges are paired, UTC nanosecond bounds with an exclusive upper end.
// Fixed-width representation preserves exact comparisons against stored evidence.
func ValidHistoryRange(from, until string) bool {
	if from == "" && until == "" {
		return true
	}
	const layout = "2006-01-02T15:04:05.000000000Z"
	start, e1 := time.Parse(layout, from)
	end, e2 := time.Parse(layout, until)
	return e1 == nil && e2 == nil && start.Format(layout) == from && end.Format(layout) == until && start.Before(end) && end.Sub(start) <= 24*time.Hour
}
