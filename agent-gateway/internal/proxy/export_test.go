package proxy

import (
	"context"
	"net/http"
)

// HandleForTest exposes the internal handle method for white-box testing of
// the pipeline verdict dispatch without requiring a full TLS connection.
func (p *Proxy) HandleForTest(w http.ResponseWriter, r *http.Request, host string) {
	p.handle(w, r, host)
}

// AuditRecord is the exported mirror of auditRecord for use in tests.
// Fields are a 1:1 copy so tests can inspect audit state without importing
// unexported types.
type AuditRecord struct {
	RequestID       string
	MatchedRule     string
	Verdict         string
	Injection       string
	CredentialScope string
	CredentialRef   string
	Error           string
}

// AuditFromContext retrieves the audit record stored in ctx and returns it as
// an *AuditRecord. Returns nil when no audit is present.
func AuditFromContext(ctx context.Context) *AuditRecord {
	a := auditFromContext(ctx)
	if a == nil {
		return nil
	}
	return &AuditRecord{
		RequestID:       a.RequestID,
		MatchedRule:     a.MatchedRule,
		Verdict:         a.Verdict,
		Injection:       a.Injection,
		CredentialScope: a.CredentialScope,
		CredentialRef:   a.CredentialRef,
		Error:           a.Error,
	}
}
