package store

import (
	"context"
	"fmt"
	"strings"
)

type FreshFindingCounts struct {
	Error      int `json:"error"`
	Warn       int `json:"warn"`
	Info       int `json:"info"`
	Structural int `json:"structural"`
	Heuristic  int `json:"heuristic"`
}

type DetectorOverviewRow struct {
	Detector string             `json:"detector"`
	Fresh    FreshFindingCounts `json:"fresh"`
	Coverage DetectorCoverage   `json:"coverage"`
}

func (s *Reader) DetectorOverview(ctx context.Context, fromUnix, toUnix int64, detectorNames []string) ([]DetectorOverviewRow, error) {
	if fromUnix >= toUnix {
		return nil, fmt.Errorf("range start must be before range end")
	}
	if len(detectorNames) == 0 {
		return []DetectorOverviewRow{}, nil
	}
	if len(detectorNames) > 50 {
		return nil, fmt.Errorf("too many detector names")
	}
	placeholders := make([]string, len(detectorNames))
	args := make([]any, 0, len(detectorNames)+2)
	for i, name := range detectorNames {
		placeholders[i] = "?"
		args = append(args, name)
	}
	args = append(args, fromUnix, toUnix)
	query := `
SELECT f.detector,f.severity,f.classification,COUNT(*)
FROM findings f JOIN sessions s ON s.id=f.session_id
JOIN detector_runs r ON r.session_id=f.session_id AND r.detector=f.detector
WHERE f.detector IN (` + strings.Join(placeholders, ",") + `) AND s.started_at_unix>=? AND s.started_at_unix<?
 AND f.stale=0 AND r.status='success' AND f.generation=r.generation
GROUP BY f.detector,f.severity,f.classification`
	rows, err := s.query.QueryContext(ctx, query, args...) //nolint:gosec // The dynamic fragment is a bounded placeholder list.
	if err != nil {
		return nil, fmt.Errorf("query overview findings: %w", err)
	}
	byName := make(map[string]*DetectorOverviewRow, len(detectorNames))
	for _, name := range detectorNames {
		byName[name] = &DetectorOverviewRow{Detector: truncateUTF8Bytes(name, 64)}
	}
	for rows.Next() {
		var name, severity, classification string
		var count int
		if err = rows.Scan(&name, &severity, &classification, &count); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan overview finding: %w", err)
		}
		row := byName[name]
		switch severity {
		case "error":
			row.Fresh.Error += count
		case "warn":
			row.Fresh.Warn += count
		case "info":
			row.Fresh.Info += count
		}
		switch classification {
		case "structural":
			row.Fresh.Structural += count
		case "heuristic":
			row.Fresh.Heuristic += count
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read overview findings: %w", err)
	}
	if err = rows.Close(); err != nil {
		return nil, fmt.Errorf("close overview findings: %w", err)
	}
	for _, name := range detectorNames {
		var success, failed, totalSessions int
		if err = s.query.QueryRowContext(ctx, `
SELECT COALESCE(SUM(r.status='success'),0),COALESCE(SUM(r.status='failed'),0),COUNT(s.id)
FROM sessions s LEFT JOIN detector_runs r ON r.session_id=s.id AND r.detector=?
WHERE s.started_at_unix>=? AND s.started_at_unix<?`, name, fromUnix, toUnix).Scan(&success, &failed, &totalSessions); err != nil {
			return nil, fmt.Errorf("query detector coverage: %w", err)
		}
		byName[name].Coverage = DetectorCoverage{Success: success, Failed: failed, NotRun: max(0, totalSessions-success-failed)}
	}
	out := make([]DetectorOverviewRow, 0, len(detectorNames))
	for _, name := range detectorNames {
		out = append(out, *byName[name])
	}
	return out, nil
}
