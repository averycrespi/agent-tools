package store

import (
	"context"
	"fmt"
)

type CategoryCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type DistributionBin struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type OverviewSignals struct {
	Goals            []CategoryCount   `json:"goals"`
	Stops            []CategoryCount   `json:"stops"`
	Records          []DistributionBin `json:"records"`
	Turns            []DistributionBin `json:"turns"`
	ContentTruncated bool              `json:"content_truncated"`
}

func (s *Reader) OverviewSignalSummary(ctx context.Context, fromUnix, toUnix int64) (OverviewSignals, error) {
	if fromUnix >= toUnix {
		return OverviewSignals{}, fmt.Errorf("range start must be before range end")
	}
	out := OverviewSignals{Goals: []CategoryCount{}, Stops: []CategoryCount{}, Records: emptyDistribution(), Turns: emptyDistribution()}
	goals, truncated, err := s.categoryCounts(ctx, `
WITH candidates AS (SELECT id FROM sessions WHERE started_at_unix>=? AND started_at_unix<?), values_by_session AS (
 SELECT c.id,CASE WHEN EXISTS(SELECT 1 FROM custom_state x WHERE x.session_id=c.id AND x.type='goal-state')
  THEN COALESCE(NULLIF((SELECT status FROM custom_state x WHERE x.session_id=c.id AND x.type='goal-state' ORDER BY source_line DESC,id DESC LIMIT 1),''),'cleared')
  ELSE 'absent' END value FROM candidates c)
SELECT substr(value,1,64),COUNT(*),length(value)>64 FROM values_by_session GROUP BY value ORDER BY COUNT(*) DESC,value LIMIT 21`, fromUnix, toUnix)
	if err != nil {
		return OverviewSignals{}, fmt.Errorf("query goal outcomes: %w", err)
	}
	out.Goals, out.ContentTruncated = goals, truncated
	stops, truncated, err := s.categoryCounts(ctx, `
WITH candidates AS (SELECT id FROM sessions WHERE started_at_unix>=? AND started_at_unix<?), values_by_session AS (
 SELECT c.id,COALESCE(NULLIF((SELECT stop_reason FROM messages m WHERE m.session_id=c.id AND m.role='assistant' AND m.stop_reason<>'' ORDER BY source_line DESC,id DESC LIMIT 1),''),'absent') value FROM candidates c)
SELECT substr(value,1,64),COUNT(*),length(value)>64 FROM values_by_session GROUP BY value ORDER BY COUNT(*) DESC,value LIMIT 21`, fromUnix, toUnix)
	if err != nil {
		return OverviewSignals{}, fmt.Errorf("query stop outcomes: %w", err)
	}
	out.Stops = stops
	out.ContentTruncated = out.ContentTruncated || truncated
	rows, err := s.query.QueryContext(ctx, `
WITH candidates AS (SELECT id,total_records FROM sessions WHERE started_at_unix>=? AND started_at_unix<?), turns AS (
 SELECT c.id,c.total_records,COUNT(m.id) turns FROM candidates c LEFT JOIN messages m ON m.session_id=c.id GROUP BY c.id,c.total_records)
SELECT total_records,turns FROM turns`, fromUnix, toUnix)
	if err != nil {
		return OverviewSignals{}, fmt.Errorf("query record distributions: %w", err)
	}
	for rows.Next() {
		var records, turns int
		if err = rows.Scan(&records, &turns); err != nil {
			_ = rows.Close()
			return OverviewSignals{}, fmt.Errorf("scan record distributions: %w", err)
		}
		out.Records[distributionIndex(records)].Count++
		out.Turns[distributionIndex(turns)].Count++
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return OverviewSignals{}, fmt.Errorf("read record distributions: %w", err)
	}
	if err = rows.Close(); err != nil {
		return OverviewSignals{}, fmt.Errorf("close record distributions: %w", err)
	}
	return out, nil
}

func (s *Reader) categoryCounts(ctx context.Context, query string, fromUnix, toUnix int64) ([]CategoryCount, bool, error) {
	rows, err := s.query.QueryContext(ctx, query, fromUnix, toUnix)
	if err != nil {
		return nil, false, err
	}
	out := []CategoryCount{}
	truncated := false
	for rows.Next() {
		var category CategoryCount
		var contentTruncated bool
		if err = rows.Scan(&category.Value, &category.Count, &contentTruncated); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		out = append(out, category)
		truncated = truncated || contentTruncated
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, false, err
	}
	if err = rows.Close(); err != nil {
		return nil, false, err
	}
	if len(out) > 20 {
		out = out[:20]
		truncated = true
	}
	return out, truncated, nil
}

func emptyDistribution() []DistributionBin {
	return []DistributionBin{{Label: "0"}, {Label: "1–10"}, {Label: "11–50"}, {Label: "51–100"}, {Label: "101–250"}, {Label: "251–500"}, {Label: "501–1000"}, {Label: "1001+"}}
}

func distributionIndex(value int) int {
	switch {
	case value <= 0:
		return 0
	case value <= 10:
		return 1
	case value <= 50:
		return 2
	case value <= 100:
		return 3
	case value <= 250:
		return 4
	case value <= 500:
		return 5
	case value <= 1000:
		return 6
	default:
		return 7
	}
}
