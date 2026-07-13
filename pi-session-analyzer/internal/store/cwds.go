package store

import (
	"context"
	"fmt"
)

const maxCWDOptions = 100

type CWDOption struct {
	CWD      string `json:"cwd"`
	Sessions int    `json:"sessions"`
}

type CWDOptions struct {
	Values    []CWDOption `json:"values"`
	Truncated bool        `json:"truncated"`
}

// DistinctCWDs lists the working directories usable as matrix filters,
// busiest first. Directories longer than the 256-byte filter bound are
// excluded because they can never match a valid filter.
func (s *Reader) DistinctCWDs(ctx context.Context) (CWDOptions, error) {
	rows, err := s.query.QueryContext(ctx, `
SELECT cwd, COUNT(*) FROM sessions
WHERE cwd<>'' AND length(CAST(cwd AS BLOB))<=256
GROUP BY cwd ORDER BY COUNT(*) DESC, cwd LIMIT ?`, maxCWDOptions+1)
	if err != nil {
		return CWDOptions{}, fmt.Errorf("query distinct cwds: %w", err)
	}
	options := CWDOptions{Values: []CWDOption{}}
	for rows.Next() {
		var option CWDOption
		if err = rows.Scan(&option.CWD, &option.Sessions); err != nil {
			_ = rows.Close()
			return CWDOptions{}, fmt.Errorf("scan distinct cwd: %w", err)
		}
		options.Values = append(options.Values, option)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return CWDOptions{}, fmt.Errorf("read distinct cwds: %w", err)
	}
	if err = rows.Close(); err != nil {
		return CWDOptions{}, fmt.Errorf("close distinct cwds: %w", err)
	}
	if len(options.Values) > maxCWDOptions {
		options.Values = options.Values[:maxCWDOptions]
		options.Truncated = true
	}
	return options, nil
}
