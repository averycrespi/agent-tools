package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type DetectorStatus struct {
	Detector   string `json:"detector"`
	Status     string `json:"status"`
	Generation int    `json:"generation"`
	Error      string `json:"error,omitempty"`
}

type DiagnosticQuery struct {
	FreshOffset int
	StaleOffset int
	Limit       int
}

type SessionDiagnosticState struct {
	FreshFindings    []FindingRow     `json:"fresh_findings"`
	FreshTotal       int              `json:"fresh_total"`
	FreshOffset      int              `json:"fresh_offset"`
	FreshTruncated   bool             `json:"fresh_truncated"`
	ContentTruncated bool             `json:"content_truncated"`
	StaleEvidence    []FindingRow     `json:"stale_evidence"`
	StaleTotal       int              `json:"stale_total"`
	StaleOffset      int              `json:"stale_offset"`
	StaleTruncated   bool             `json:"stale_truncated"`
	Detectors        []DetectorStatus `json:"detectors"`
}

func (s *Reader) SessionDiagnostics(ctx context.Context, prefix string, detectorNames []string) (SessionDiagnosticState, error) {
	return s.SessionDiagnosticsPage(ctx, prefix, detectorNames, DiagnosticQuery{})
}

func (s *Reader) SessionDiagnosticsPage(ctx context.Context, prefix string, detectorNames []string, q DiagnosticQuery) (SessionDiagnosticState, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return SessionDiagnosticState{}, err
	}
	if q.FreshOffset < 0 {
		q.FreshOffset = 0
	}
	if q.StaleOffset < 0 {
		q.StaleOffset = 0
	}
	if q.Limit <= 0 || q.Limit > 25 {
		q.Limit = 25
	}
	placeholders := make([]string, len(detectorNames))
	registryArgs := make([]any, len(detectorNames))
	for i, name := range detectorNames {
		placeholders[i], registryArgs[i] = "?", name
	}
	freshRegistry, retiredCondition := "0", "1"
	if len(placeholders) > 0 {
		list := strings.Join(placeholders, ",")
		freshRegistry = "f.detector IN (" + list + ")"
		retiredCondition = "f.detector NOT IN (" + list + ")"
	}
	freshCondition := freshRegistry + " AND f.stale=0 AND r.status='success' AND f.generation=r.generation"
	staleCondition := retiredCondition + " OR NOT (f.stale=0 AND COALESCE(r.status,'')='success' AND f.generation=r.generation)"
	out := SessionDiagnosticState{FreshOffset: q.FreshOffset, StaleOffset: q.StaleOffset, FreshFindings: []FindingRow{}, StaleEvidence: []FindingRow{}, Detectors: []DetectorStatus{}}
	freshCountArgs := append([]any{id}, registryArgs...)
	if err = s.query.QueryRowContext(ctx, `SELECT COUNT(*) FROM findings f JOIN detector_runs r ON r.session_id=f.session_id AND r.detector=f.detector WHERE f.session_id=? AND `+freshCondition, freshCountArgs...).Scan(&out.FreshTotal); err != nil { //nolint:gosec // Dynamic fragments contain only bounded placeholders and fixed predicates.
		return SessionDiagnosticState{}, fmt.Errorf("count fresh findings: %w", err)
	}
	freshArgs := append(append([]any{id}, registryArgs...), q.Limit, q.FreshOffset)
	rows, err := s.query.QueryContext(ctx, `
SELECT CAST(f.id AS TEXT),f.session_id,f.detector,f.classification,f.severity,f.summary,
 f.first_evidence_id,f.details,f.source_line,f.generation,f.stale,r.status,r.error_summary
FROM findings f JOIN detector_runs r ON r.session_id=f.session_id AND r.detector=f.detector
WHERE f.session_id=? AND `+freshCondition+`
ORDER BY f.detector,f.first_evidence_id LIMIT ? OFFSET ?`, freshArgs...) //nolint:gosec // Dynamic fragments contain only bounded placeholders and fixed predicates.
	if err != nil {
		return SessionDiagnosticState{}, fmt.Errorf("query fresh findings: %w", err)
	}
	out.FreshFindings, err = scanFindingRows(rows)
	if err != nil {
		return SessionDiagnosticState{}, err
	}
	out.FreshTruncated = q.FreshOffset+len(out.FreshFindings) < out.FreshTotal
	staleCountArgs := append([]any{id}, registryArgs...)
	if err = s.query.QueryRowContext(ctx, `SELECT COUNT(*) FROM findings f LEFT JOIN detector_runs r ON r.session_id=f.session_id AND r.detector=f.detector WHERE f.session_id=? AND (`+staleCondition+`)`, staleCountArgs...).Scan(&out.StaleTotal); err != nil { //nolint:gosec // Dynamic fragments contain only bounded placeholders and fixed predicates.
		return SessionDiagnosticState{}, fmt.Errorf("count stale findings: %w", err)
	}
	staleArgs := append(append([]any{id}, registryArgs...), q.Limit, q.StaleOffset)
	rows, err = s.query.QueryContext(ctx, `
SELECT CAST(f.id AS TEXT),f.session_id,f.detector,f.classification,f.severity,f.summary,
 f.first_evidence_id,f.details,f.source_line,f.generation,f.stale,COALESCE(r.status,''),COALESCE(r.error_summary,'')
FROM findings f LEFT JOIN detector_runs r ON r.session_id=f.session_id AND r.detector=f.detector
WHERE f.session_id=? AND (`+staleCondition+`)
ORDER BY f.detector,f.first_evidence_id LIMIT ? OFFSET ?`, staleArgs...) //nolint:gosec // Dynamic fragments contain only bounded placeholders and fixed predicates.
	if err != nil {
		return SessionDiagnosticState{}, fmt.Errorf("query stale evidence: %w", err)
	}
	out.StaleEvidence, err = scanFindingRows(rows)
	if err != nil {
		return SessionDiagnosticState{}, err
	}
	out.StaleTruncated = q.StaleOffset+len(out.StaleEvidence) < out.StaleTotal
	for _, findings := range [][]FindingRow{out.FreshFindings, out.StaleEvidence} {
		for i := range findings {
			boundedSummary := truncateUTF8Bytes(findings[i].Summary, 64)
			boundedDetails := truncateUTF8Bytes(findings[i].Details, 64)
			boundedError := truncateUTF8Bytes(findings[i].RunError, 64)
			out.ContentTruncated = out.ContentTruncated || boundedSummary != findings[i].Summary || boundedDetails != findings[i].Details || boundedError != findings[i].RunError
			findings[i].Summary, findings[i].Details, findings[i].RunError = boundedSummary, boundedDetails, boundedError
		}
	}
	runs := map[string]DetectorStatus{}
	if len(detectorNames) > 0 {
		runArgs := append([]any{id}, registryArgs...)
		rows, err = s.query.QueryContext(ctx, `SELECT detector,status,generation,error_summary FROM detector_runs WHERE session_id=? AND detector IN (`+strings.Join(placeholders, ",")+`) ORDER BY detector`, runArgs...) //nolint:gosec // Dynamic fragment is a bounded placeholder list.
		if err != nil {
			return SessionDiagnosticState{}, fmt.Errorf("query detector status: %w", err)
		}
		for rows.Next() {
			var status DetectorStatus
			if err = rows.Scan(&status.Detector, &status.Status, &status.Generation, &status.Error); err != nil {
				_ = rows.Close()
				return SessionDiagnosticState{}, fmt.Errorf("scan detector status: %w", err)
			}
			runs[status.Detector] = status
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			return SessionDiagnosticState{}, fmt.Errorf("read detector status: %w", err)
		}
		if err = rows.Close(); err != nil {
			return SessionDiagnosticState{}, fmt.Errorf("close detector status: %w", err)
		}
	}
	for _, name := range detectorNames {
		if _, exists := runs[name]; !exists {
			runs[name] = DetectorStatus{Detector: name, Status: "not_run"}
		}
	}
	names := make([]string, 0, len(runs))
	for name := range runs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		status := runs[name]
		boundedDetector := truncateUTF8Bytes(status.Detector, 64)
		boundedError := truncateUTF8Bytes(status.Error, 64)
		out.ContentTruncated = out.ContentTruncated || boundedDetector != status.Detector || boundedError != status.Error
		status.Detector, status.Error = boundedDetector, boundedError
		out.Detectors = append(out.Detectors, status)
	}
	return out, nil
}

func scanFindingRows(rows Rows) ([]FindingRow, error) {
	defer func() { _ = rows.Close() }()
	out := []FindingRow{}
	for rows.Next() {
		var finding FindingRow
		if err := rows.Scan(&finding.ID, &finding.SessionID, &finding.Detector, &finding.Classification, &finding.Severity, &finding.Summary, &finding.EvidenceID, &finding.Details, &finding.SourceLine, &finding.Generation, &finding.Stale, &finding.RunStatus, &finding.RunError); err != nil {
			return nil, fmt.Errorf("scan finding: %w", err)
		}
		out = append(out, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read findings: %w", err)
	}
	return out, nil
}
