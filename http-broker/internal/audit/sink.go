package audit

import (
	"log/slog"

	"github.com/averycrespi/agent-tools/http-broker/internal/proxy"
)

// Sink adapts a Logger to the proxy's AuditSink interface.
//
// The adapter lives here rather than in the proxy so the proxy depends on a
// one-method interface it defines itself, and can be tested with a recorder
// that never touches SQLite.
type Sink struct {
	logger *Logger
	log    *slog.Logger
}

// NewSink returns a proxy.AuditSink backed by logger.
func NewSink(logger *Logger, log *slog.Logger) *Sink {
	return &Sink{logger: logger, log: log}
}

// Record implements proxy.AuditSink.
//
// A write failure is logged and discarded. The pipeline must not fail a
// request because auditing failed — the audit log exists to observe traffic,
// not to gate it.
func (s *Sink) Record(e proxy.Event) {
	if err := s.logger.Record(fromEvent(e)); err != nil && s.log != nil {
		s.log.Error("audit write failed", "error", err)
	}
}

// fromEvent converts a proxy event into a stored record.
func fromEvent(e proxy.Event) Record {
	return Record{
		ID:            e.ID,
		Timestamp:     e.Start,
		Interception:  e.Interception,
		Method:        e.Method,
		Host:          e.Host,
		Port:          e.Port,
		Path:          e.Path,
		Query:         e.Query,
		Status:        e.Status,
		DurationMS:    e.DurationMS,
		BytesIn:       e.BytesIn,
		BytesOut:      e.BytesOut,
		MatchedRule:   e.MatchedRule,
		Mode:          e.Mode,
		Injection:     e.Injection,
		CredentialRef: e.CredentialRef,
		Outcome:       e.Outcome,
		Error:         e.Error,
	}
}
