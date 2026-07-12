package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const MaxOverviewBuckets = 90

type BucketUnit string

const (
	BucketDay   BucketUnit = "day"
	BucketWeek  BucketUnit = "week"
	BucketMonth BucketUnit = "month"
	BucketYear  BucketUnit = "year"
)

type CalendarBucket struct {
	Key       string `json:"key"`
	StartUnix int64  `json:"start_unix"`
	EndUnix   int64  `json:"end_unix"`
	Partial   bool   `json:"partial"`
}

type OverviewQuery struct {
	Timezone      string
	Unit          BucketUnit
	Buckets       []CalendarBucket
	DetectorNames []string
}

type GoalOutcomeCounts struct {
	Absent   int `json:"absent"`
	Cleared  int `json:"cleared"`
	Active   int `json:"active"`
	Complete int `json:"complete"`
	Other    int `json:"other"`
}

type OverviewTimeline struct {
	Keys      []string `json:"keys"`
	StartUnix []int64  `json:"start_unix"`
	EndUnix   []int64  `json:"end_unix"`
	Partial   []bool   `json:"partial"`
	Sessions  []int    `json:"sessions"`
}

type OverviewBucketSignals struct {
	Cost             []float64 `json:"cost_as_logged"`
	ToolCalls        []int     `json:"tool_calls"`
	Compactions      []int     `json:"compactions"`
	BrokerGuards     []int     `json:"broker_guards"`
	OutputTokens     []int64   `json:"output_tokens"`
	ReasoningTokens  []int64   `json:"reasoning_tokens"`
	CacheReadTokens  []int64   `json:"cache_read_tokens"`
	CacheWriteTokens []int64   `json:"cache_write_tokens"`
	FreshError       []int     `json:"fresh_error"`
	FreshWarn        []int     `json:"fresh_warn"`
	FreshInfo        []int     `json:"fresh_info"`
	FreshStructural  []int     `json:"fresh_structural"`
	FreshHeuristic   []int     `json:"fresh_heuristic"`
	DetectorSuccess  []int     `json:"detector_success"`
	DetectorFailed   []int     `json:"detector_failed"`
	DetectorNotRun   []int     `json:"detector_not_run"`
	GoalAbsent       []int     `json:"goal_absent"`
	GoalCleared      []int     `json:"goal_cleared"`
	GoalActive       []int     `json:"goal_active"`
	GoalComplete     []int     `json:"goal_complete"`
	GoalOther        []int     `json:"goal_other"`
}

type OverviewBucket struct {
	Key              string             `json:"key"`
	StartUnix        int64              `json:"start_unix"`
	EndUnix          int64              `json:"end_unix"`
	Partial          bool               `json:"partial"`
	Sessions         int                `json:"sessions"`
	Cost             float64            `json:"cost_as_logged"`
	ToolCalls        int                `json:"tool_calls"`
	Compactions      int                `json:"compactions"`
	BrokerGuards     int                `json:"broker_guards"`
	OutputTokens     int64              `json:"output_tokens"`
	ReasoningTokens  int64              `json:"reasoning_tokens"`
	CacheReadTokens  int64              `json:"cache_read_tokens"`
	CacheWriteTokens int64              `json:"cache_write_tokens"`
	FreshFindings    FreshFindingCounts `json:"-"`
	DetectorCoverage DetectorCoverage   `json:"-"`
	GoalOutcomes     GoalOutcomeCounts  `json:"-"`
}

type Overview struct {
	Timezone        string                 `json:"timezone"`
	Unit            BucketUnit             `json:"bucket"`
	UntimedSessions int                    `json:"untimed_sessions"`
	IndexedAt       string                 `json:"indexed_at"`
	Buckets         []OverviewBucket       `json:"buckets"`
	Timeline        OverviewTimeline       `json:"-"`
	ToolOutcomes    ToolOutcomeReport      `json:"tool_outcomes"`
	Detectors       []DetectorOverviewRow  `json:"detectors"`
	Signals         OverviewSignals        `json:"signals"`
	StopReasons     []CategoryBucketSeries `json:"-"`
	BucketSignals   OverviewBucketSignals  `json:"-"`
}

func CalendarBuckets(from, to, now time.Time, location *time.Location, unit BucketUnit) ([]CalendarBucket, error) {
	if location == nil {
		return nil, fmt.Errorf("timezone is required")
	}
	if !from.Before(to) {
		return nil, fmt.Errorf("range start must be before range end")
	}
	if unit != BucketDay && unit != BucketWeek && unit != BucketMonth && unit != BucketYear {
		return nil, fmt.Errorf("bucket must be day, week, month, or auto-resolved year")
	}
	cursor := from.In(location)
	end := to.In(location)
	buckets := make([]CalendarBucket, 0)
	for cursor.Before(end) {
		bucketStart := calendarBucketStart(cursor, location, unit)
		var next time.Time
		switch unit {
		case BucketDay:
			next = bucketStart.AddDate(0, 0, 1)
		case BucketWeek:
			next = bucketStart.AddDate(0, 0, 7)
		case BucketMonth:
			next = bucketStart.AddDate(0, 1, 0)
		case BucketYear:
			next = bucketStart.AddDate(1, 0, 0)
		}
		if next.After(end) {
			next = end
		}
		buckets = append(buckets, CalendarBucket{
			Key:       bucketKey(bucketStart, unit),
			StartUnix: cursor.Unix(),
			EndUnix:   next.Unix(),
			Partial:   !now.Before(cursor) && now.Before(next),
		})
		if len(buckets) > MaxOverviewBuckets {
			return nil, fmt.Errorf("too many buckets; choose a coarser bucket or shorter range")
		}
		cursor = next
	}
	return buckets, nil
}

func calendarBucketStart(value time.Time, location *time.Location, unit BucketUnit) time.Time {
	local := value.In(location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	switch unit {
	case BucketWeek:
		daysSinceMonday := (int(start.Weekday()) + 6) % 7
		return start.AddDate(0, 0, -daysSinceMonday)
	case BucketMonth:
		return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location)
	case BucketYear:
		return time.Date(local.Year(), 1, 1, 0, 0, 0, 0, location)
	default:
		return start
	}
}

func bucketKey(start time.Time, unit BucketUnit) string {
	switch unit {
	case BucketWeek:
		year, week := start.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week)
	case BucketMonth:
		return start.Format("2006-01")
	case BucketYear:
		return start.Format("2006")
	default:
		return start.Format("2006-01-02")
	}
}

func (s *Reader) Overview(ctx context.Context, q OverviewQuery) (Overview, error) {
	if len(q.Buckets) == 0 || len(q.Buckets) > MaxOverviewBuckets {
		return Overview{}, fmt.Errorf("overview requires between 1 and %d buckets", MaxOverviewBuckets)
	}
	placeholders := make([]string, len(q.Buckets))
	args := make([]any, 0, len(q.Buckets)*4)
	for i, bucket := range q.Buckets {
		if bucket.StartUnix >= bucket.EndUnix {
			return Overview{}, fmt.Errorf("bucket %q start must be before end", bucket.Key)
		}
		placeholders[i] = "(?,?,?,?)"
		args = append(args, bucket.Key, bucket.StartUnix, bucket.EndUnix, bucket.Partial)
	}
	detectorFilter, freshDetectorFilter := "0", "0"
	if len(q.DetectorNames) > 0 {
		if len(q.DetectorNames) > 50 {
			return Overview{}, fmt.Errorf("too many detector names")
		}
		detectorPlaceholders := make([]string, len(q.DetectorNames))
		for i, name := range q.DetectorNames {
			detectorPlaceholders[i] = "?"
			args = append(args, name)
		}
		detectorFilter = "detector IN (" + strings.Join(detectorPlaceholders, ",") + ")"
		freshDetectorFilter = "f.detector IN (" + strings.Join(detectorPlaceholders, ",") + ")"
		for _, name := range q.DetectorNames {
			args = append(args, name)
		}
	}
	query := `
WITH buckets(key,start_unix,end_unix,partial) AS (VALUES ` + strings.Join(placeholders, ",") + `),
message_facts AS (
 SELECT session_id,COALESCE(SUM(cost),0) cost,COALESCE(SUM(output_tokens),0) output_tokens,
  COALESCE(SUM(reasoning_tokens),0) reasoning_tokens,COALESCE(SUM(cache_read_tokens),0) cache_read_tokens,
  COALESCE(SUM(cache_write_tokens),0) cache_write_tokens
 FROM messages GROUP BY session_id
),
call_facts AS (SELECT session_id,COUNT(*) calls FROM tool_calls GROUP BY session_id),
event_facts AS (SELECT session_id,COUNT(*) compactions FROM events WHERE type='compaction' GROUP BY session_id),
guard_facts AS (SELECT session_id,COUNT(*) guards FROM custom_messages WHERE type='broker-guard' GROUP BY session_id),
fresh_facts AS (
 SELECT f.session_id,SUM(f.severity='error') errors,SUM(f.severity='warn') warns,SUM(f.severity='info') infos,
  SUM(f.classification='structural') structural,SUM(f.classification='heuristic') heuristic
 FROM findings f JOIN detector_runs r ON r.session_id=f.session_id AND r.detector=f.detector
 WHERE f.stale=0 AND r.status='success' AND f.generation=r.generation AND ` + freshDetectorFilter + ` GROUP BY f.session_id
), run_facts AS (
 SELECT session_id,SUM(status='success') successes,SUM(status='failed') failures FROM detector_runs
 WHERE ` + detectorFilter + ` GROUP BY session_id
), goal_facts AS (
 SELECT s.id session_id,CASE
  WHEN NOT EXISTS(SELECT 1 FROM custom_state x WHERE x.session_id=s.id AND x.type='goal-state') THEN 'absent'
  WHEN COALESCE((SELECT status FROM custom_state x WHERE x.session_id=s.id AND x.type='goal-state' ORDER BY source_line DESC,id DESC LIMIT 1),'')='' THEN 'cleared'
  ELSE (SELECT status FROM custom_state x WHERE x.session_id=s.id AND x.type='goal-state' ORDER BY source_line DESC,id DESC LIMIT 1) END outcome
 FROM sessions s
)
SELECT b.key,b.start_unix,b.end_unix,b.partial,COUNT(s.id),
 COALESCE(SUM(m.cost),0),COALESCE(SUM(m.output_tokens),0),COALESCE(SUM(m.reasoning_tokens),0),
 COALESCE(SUM(m.cache_read_tokens),0),COALESCE(SUM(m.cache_write_tokens),0),
 COALESCE(SUM(c.calls),0),COALESCE(SUM(e.compactions),0),COALESCE(SUM(g.guards),0),
 COALESCE(SUM(f.errors),0),COALESCE(SUM(f.warns),0),COALESCE(SUM(f.infos),0),COALESCE(SUM(f.structural),0),COALESCE(SUM(f.heuristic),0),
 COALESCE(SUM(r.successes),0),COALESCE(SUM(r.failures),0),MAX(0,COUNT(s.id)*` + fmt.Sprint(len(q.DetectorNames)) + `-COALESCE(SUM(r.successes),0)-COALESCE(SUM(r.failures),0)),
 SUM(CASE WHEN goal.outcome='absent' THEN 1 ELSE 0 END),SUM(CASE WHEN goal.outcome='cleared' THEN 1 ELSE 0 END),
 SUM(CASE WHEN goal.outcome='active' THEN 1 ELSE 0 END),SUM(CASE WHEN goal.outcome IN ('complete','completed','done') THEN 1 ELSE 0 END),
 SUM(CASE WHEN goal.outcome NOT IN ('absent','cleared','active','complete','completed','done') THEN 1 ELSE 0 END)
FROM buckets b
LEFT JOIN sessions s ON s.started_at_unix>=b.start_unix AND s.started_at_unix<b.end_unix
LEFT JOIN message_facts m ON m.session_id=s.id
LEFT JOIN call_facts c ON c.session_id=s.id
LEFT JOIN event_facts e ON e.session_id=s.id
LEFT JOIN guard_facts g ON g.session_id=s.id
LEFT JOIN fresh_facts f ON f.session_id=s.id
LEFT JOIN run_facts r ON r.session_id=s.id
LEFT JOIN goal_facts goal ON goal.session_id=s.id
GROUP BY b.key,b.start_unix,b.end_unix,b.partial
ORDER BY b.start_unix`
	rows, err := s.query.QueryContext(ctx, query, args...) //nolint:gosec // Only a bounded internal VALUES placeholder list is dynamic.
	if err != nil {
		return Overview{}, fmt.Errorf("query overview: %w", err)
	}
	defer func() { _ = rows.Close() }()
	overview := Overview{Timezone: q.Timezone, Unit: q.Unit, Buckets: []OverviewBucket{}}
	for rows.Next() {
		var bucket OverviewBucket
		if err = rows.Scan(
			&bucket.Key, &bucket.StartUnix, &bucket.EndUnix, &bucket.Partial, &bucket.Sessions,
			&bucket.Cost, &bucket.OutputTokens, &bucket.ReasoningTokens, &bucket.CacheReadTokens,
			&bucket.CacheWriteTokens, &bucket.ToolCalls, &bucket.Compactions, &bucket.BrokerGuards,
			&bucket.FreshFindings.Error, &bucket.FreshFindings.Warn, &bucket.FreshFindings.Info, &bucket.FreshFindings.Structural, &bucket.FreshFindings.Heuristic,
			&bucket.DetectorCoverage.Success, &bucket.DetectorCoverage.Failed, &bucket.DetectorCoverage.NotRun,
			&bucket.GoalOutcomes.Absent, &bucket.GoalOutcomes.Cleared, &bucket.GoalOutcomes.Active, &bucket.GoalOutcomes.Complete, &bucket.GoalOutcomes.Other,
		); err != nil {
			return Overview{}, fmt.Errorf("scan overview: %w", err)
		}
		overview.Buckets = append(overview.Buckets, bucket)
	}
	if err = rows.Err(); err != nil {
		return Overview{}, fmt.Errorf("read overview: %w", err)
	}
	for _, bucket := range overview.Buckets {
		overview.Timeline.Keys = append(overview.Timeline.Keys, bucket.Key)
		overview.Timeline.StartUnix = append(overview.Timeline.StartUnix, bucket.StartUnix)
		overview.Timeline.EndUnix = append(overview.Timeline.EndUnix, bucket.EndUnix)
		overview.Timeline.Partial = append(overview.Timeline.Partial, bucket.Partial)
		overview.Timeline.Sessions = append(overview.Timeline.Sessions, bucket.Sessions)
		overview.BucketSignals.Cost = append(overview.BucketSignals.Cost, bucket.Cost)
		overview.BucketSignals.ToolCalls = append(overview.BucketSignals.ToolCalls, bucket.ToolCalls)
		overview.BucketSignals.Compactions = append(overview.BucketSignals.Compactions, bucket.Compactions)
		overview.BucketSignals.BrokerGuards = append(overview.BucketSignals.BrokerGuards, bucket.BrokerGuards)
		overview.BucketSignals.OutputTokens = append(overview.BucketSignals.OutputTokens, bucket.OutputTokens)
		overview.BucketSignals.ReasoningTokens = append(overview.BucketSignals.ReasoningTokens, bucket.ReasoningTokens)
		overview.BucketSignals.CacheReadTokens = append(overview.BucketSignals.CacheReadTokens, bucket.CacheReadTokens)
		overview.BucketSignals.CacheWriteTokens = append(overview.BucketSignals.CacheWriteTokens, bucket.CacheWriteTokens)
		overview.BucketSignals.FreshError = append(overview.BucketSignals.FreshError, bucket.FreshFindings.Error)
		overview.BucketSignals.FreshWarn = append(overview.BucketSignals.FreshWarn, bucket.FreshFindings.Warn)
		overview.BucketSignals.FreshInfo = append(overview.BucketSignals.FreshInfo, bucket.FreshFindings.Info)
		overview.BucketSignals.FreshStructural = append(overview.BucketSignals.FreshStructural, bucket.FreshFindings.Structural)
		overview.BucketSignals.FreshHeuristic = append(overview.BucketSignals.FreshHeuristic, bucket.FreshFindings.Heuristic)
		overview.BucketSignals.DetectorSuccess = append(overview.BucketSignals.DetectorSuccess, bucket.DetectorCoverage.Success)
		overview.BucketSignals.DetectorFailed = append(overview.BucketSignals.DetectorFailed, bucket.DetectorCoverage.Failed)
		overview.BucketSignals.DetectorNotRun = append(overview.BucketSignals.DetectorNotRun, bucket.DetectorCoverage.NotRun)
		overview.BucketSignals.GoalAbsent = append(overview.BucketSignals.GoalAbsent, bucket.GoalOutcomes.Absent)
		overview.BucketSignals.GoalCleared = append(overview.BucketSignals.GoalCleared, bucket.GoalOutcomes.Cleared)
		overview.BucketSignals.GoalActive = append(overview.BucketSignals.GoalActive, bucket.GoalOutcomes.Active)
		overview.BucketSignals.GoalComplete = append(overview.BucketSignals.GoalComplete, bucket.GoalOutcomes.Complete)
		overview.BucketSignals.GoalOther = append(overview.BucketSignals.GoalOther, bucket.GoalOutcomes.Other)
	}
	if err = s.query.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE started_at_unix IS NULL),COALESCE(MAX(ingested_at),'') FROM sessions`).Scan(&overview.UntimedSessions, &overview.IndexedAt); err != nil {
		return Overview{}, fmt.Errorf("query index status: %w", err)
	}
	fromUnix, toUnix := q.Buckets[0].StartUnix, q.Buckets[len(q.Buckets)-1].EndUnix
	overview.ToolOutcomes, err = s.ToolOutcomeRange(ctx, fromUnix, toUnix)
	if err != nil {
		return Overview{}, fmt.Errorf("query overview tool outcomes: %w", err)
	}
	overview.Detectors, err = s.DetectorOverview(ctx, fromUnix, toUnix, q.DetectorNames)
	if err != nil {
		return Overview{}, fmt.Errorf("query overview detectors: %w", err)
	}
	overview.Signals, err = s.OverviewSignalSummary(ctx, fromUnix, toUnix)
	if err != nil {
		return Overview{}, fmt.Errorf("query overview signals: %w", err)
	}
	stopCategories := make([]string, 0, min(10, len(overview.Signals.Stops)))
	for i := 0; i < len(overview.Signals.Stops) && i < 10; i++ {
		stopCategories = append(stopCategories, overview.Signals.Stops[i].Value)
	}
	overview.StopReasons, err = s.StopReasonBucketSeries(ctx, q.Buckets, stopCategories)
	if err != nil {
		return Overview{}, fmt.Errorf("query overview stop series: %w", err)
	}
	return overview, nil
}
