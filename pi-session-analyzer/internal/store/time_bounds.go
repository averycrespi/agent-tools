package store

import (
	"context"
	"database/sql"
	"fmt"
)

type CanonicalTimeRange struct {
	Minimum *int64
	Maximum *int64
}

func (s *Reader) CanonicalTimeBounds(ctx context.Context) (CanonicalTimeRange, error) {
	var minimum, maximum sql.NullInt64
	if err := s.query.QueryRowContext(ctx, `SELECT MIN(started_at_unix),MAX(started_at_unix) FROM sessions WHERE started_at_unix IS NOT NULL`).Scan(&minimum, &maximum); err != nil {
		return CanonicalTimeRange{}, fmt.Errorf("query canonical time bounds: %w", err)
	}
	out := CanonicalTimeRange{}
	if minimum.Valid {
		out.Minimum = &minimum.Int64
		out.Maximum = &maximum.Int64
	}
	return out, nil
}
