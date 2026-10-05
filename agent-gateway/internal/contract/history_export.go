package contract

// HistoryExport is one bounded, materialized read transaction, not a reusable
// snapshot or an execution audit. Each subsequent export observes a new snapshot.
type HistoryExport struct {
	Format               int                   `json:"format"`
	InstallationID       string                `json:"installation_id"`
	Generation           string                `json:"generation"`
	CapturedAt           string                `json:"captured_at"`
	HighWater            string                `json:"high_water"`
	Pruning              string                `json:"pruning"`
	Retained             int64                 `json:"retained"`
	AfterSequence        string                `json:"after_sequence"`
	NextSequence         string                `json:"next_sequence"`
	Truncated            bool                  `json:"truncated"`
	CompleteTrafficAudit bool                  `json:"complete_traffic_audit"`
	Absence              string                `json:"absence"`
	Records              []HistoryExportRecord `json:"records"`
}

type HistoryExportRecord struct {
	Sequence string             `json:"sequence"`
	Protocol string             `json:"protocol"`
	MCP      *Invocation        `json:"mcp,omitempty"`
	HTTP     *HTTPTrafficRecord `json:"http,omitempty"`
	Git      *GitTrafficRecord  `json:"git,omitempty"`
}

const (
	HistoryExportMaxRecords = 256
	HistoryExportMaxBytes   = 900 * 1024
	HistoryExportAbsence    = "Absent records do not establish nonexecution; missing completion remains unknown. Each response is a new snapshot of rolling best-effort history."
)
