package store

import (
	"context"
	"fmt"
	"strings"
)

type CategoryBucketSeries struct {
	Value  string `json:"value"`
	Counts []int  `json:"counts"`
}

func (s *Reader) StopReasonBucketSeries(ctx context.Context, buckets []CalendarBucket, categories []string) ([]CategoryBucketSeries, error) {
	if len(buckets) == 0 || len(buckets) > MaxOverviewBuckets || len(categories) == 0 {
		return []CategoryBucketSeries{}, nil
	}
	if len(categories) > 10 {
		categories = categories[:10]
	}
	bucketPlaceholders := make([]string, len(buckets))
	categoryPlaceholders := make([]string, len(categories))
	args := make([]any, 0, len(buckets)*3+len(categories))
	for i, bucket := range buckets {
		bucketPlaceholders[i] = "(?,?,?)"
		args = append(args, bucket.Key, bucket.StartUnix, bucket.EndUnix)
	}
	for i, category := range categories {
		categoryPlaceholders[i] = "?"
		args = append(args, category)
	}
	query := `
WITH buckets(key,start_unix,end_unix) AS (VALUES ` + strings.Join(bucketPlaceholders, ",") + `), session_stops AS (
 SELECT s.id,s.started_at_unix,COALESCE(NULLIF((SELECT stop_reason FROM messages m WHERE m.session_id=s.id AND m.role='assistant' AND m.stop_reason<>'' ORDER BY source_line DESC,id DESC LIMIT 1),''),'absent') value
 FROM sessions s WHERE s.started_at_unix>=? AND s.started_at_unix<?
)
SELECT b.key,COALESCE(substr(ss.value,1,64),''),COUNT(*) FROM buckets b JOIN session_stops ss ON ss.started_at_unix>=b.start_unix AND ss.started_at_unix<b.end_unix
WHERE COALESCE(substr(ss.value,1,64),'') IN (` + strings.Join(categoryPlaceholders, ",") + `) GROUP BY b.key,COALESCE(substr(ss.value,1,64),'')`
	// The outer range avoids scanning sessions outside the bucket union.
	categoryArgs := append([]any(nil), args[len(buckets)*3:]...)
	args = args[:len(buckets)*3]
	args = append(args, buckets[0].StartUnix, buckets[len(buckets)-1].EndUnix)
	args = append(args, categoryArgs...)
	rows, err := s.query.QueryContext(ctx, query, args...) //nolint:gosec // Dynamic fragments are bounded placeholder lists.
	if err != nil {
		return nil, fmt.Errorf("query stop-reason bucket series: %w", err)
	}
	index := make(map[string]int, len(buckets))
	for i, bucket := range buckets {
		index[bucket.Key] = i
	}
	byCategory := make(map[string]*CategoryBucketSeries, len(categories))
	for _, category := range categories {
		byCategory[category] = &CategoryBucketSeries{Value: category, Counts: make([]int, len(buckets))}
	}
	for rows.Next() {
		var key, category string
		var count int
		if err = rows.Scan(&key, &category, &count); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan stop-reason bucket: %w", err)
		}
		if series := byCategory[category]; series != nil {
			series.Counts[index[key]] = count
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read stop-reason buckets: %w", err)
	}
	if err = rows.Close(); err != nil {
		return nil, fmt.Errorf("close stop-reason buckets: %w", err)
	}
	out := make([]CategoryBucketSeries, 0, len(categories))
	for _, category := range categories {
		out = append(out, *byCategory[category])
	}
	return out, nil
}
