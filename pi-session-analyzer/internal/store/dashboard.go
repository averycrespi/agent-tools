package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const MaxOverviewBuckets = 400

type BucketUnit string

const (
	BucketDay   BucketUnit = "day"
	BucketWeek  BucketUnit = "week"
	BucketMonth BucketUnit = "month"
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

type OverviewBucket struct {
	Key              string  `json:"key"`
	StartUnix        int64   `json:"start_unix"`
	EndUnix          int64   `json:"end_unix"`
	Partial          bool    `json:"partial"`
	Sessions         int     `json:"sessions"`
	Cost             float64 `json:"cost_as_logged"`
	ToolCalls        int     `json:"tool_calls"`
	Compactions      int     `json:"compactions"`
	BrokerGuards     int     `json:"broker_guards"`
	OutputTokens     int64   `json:"output_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
}

type Overview struct {
	Timezone        string                `json:"timezone"`
	Unit            BucketUnit            `json:"bucket"`
	UntimedSessions int                   `json:"untimed_sessions"`
	Buckets         []OverviewBucket      `json:"buckets"`
	ToolOutcomes    ToolOutcomeReport     `json:"tool_outcomes"`
	Detectors       []DetectorOverviewRow `json:"detectors"`
	Signals         OverviewSignals       `json:"signals"`
}

func CalendarBuckets(from, to, now time.Time, location *time.Location, unit BucketUnit) ([]CalendarBucket, error) {
	if location == nil {
		return nil, fmt.Errorf("timezone is required")
	}
	if !from.Before(to) {
		return nil, fmt.Errorf("range start must be before range end")
	}
	if unit != BucketDay && unit != BucketWeek && unit != BucketMonth {
		return nil, fmt.Errorf("bucket must be day, week, or month")
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
guard_facts AS (SELECT session_id,COUNT(*) guards FROM custom_messages WHERE type='broker-guard' GROUP BY session_id)
SELECT b.key,b.start_unix,b.end_unix,b.partial,COUNT(s.id),
 COALESCE(SUM(m.cost),0),COALESCE(SUM(m.output_tokens),0),COALESCE(SUM(m.reasoning_tokens),0),
 COALESCE(SUM(m.cache_read_tokens),0),COALESCE(SUM(m.cache_write_tokens),0),
 COALESCE(SUM(c.calls),0),COALESCE(SUM(e.compactions),0),COALESCE(SUM(g.guards),0)
FROM buckets b
LEFT JOIN sessions s ON s.started_at_unix>=b.start_unix AND s.started_at_unix<b.end_unix
LEFT JOIN message_facts m ON m.session_id=s.id
LEFT JOIN call_facts c ON c.session_id=s.id
LEFT JOIN event_facts e ON e.session_id=s.id
LEFT JOIN guard_facts g ON g.session_id=s.id
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
		); err != nil {
			return Overview{}, fmt.Errorf("scan overview: %w", err)
		}
		overview.Buckets = append(overview.Buckets, bucket)
	}
	if err = rows.Err(); err != nil {
		return Overview{}, fmt.Errorf("read overview: %w", err)
	}
	if err = s.query.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE started_at_unix IS NULL`).Scan(&overview.UntimedSessions); err != nil {
		return Overview{}, fmt.Errorf("count untimed sessions: %w", err)
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
	return overview, nil
}
