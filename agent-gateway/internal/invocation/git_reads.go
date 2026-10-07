package invocation

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

func (s *ReadService) GetGit(ctx context.Context, id string) (out contract.GitTrafficRecord, err error) {
	if !validOpaqueInvocationID(id) {
		return out, ErrInvalidInput
	}
	if s.repository.traffic == nil {
		return out, ErrInvalidState
	}
	err = s.repository.traffic.view(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, _, e = scanGitTraffic(tx.QueryRowContext(ctx, gitTrafficSelect+` WHERE id=?`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		return contract.GitTrafficRecord{}, err
	}
	return
}

func (s *ReadService) ListGit(ctx context.Context, q contract.GitTrafficQuery) (page contract.GitTrafficPage, err error) {
	if q.Limit < 1 || q.Limit > 100 || !validGitTrafficFilters(q.Filters) {
		return page, ErrInvalidInput
	}
	if s.repository.traffic == nil {
		return page, ErrInvalidState
	}
	cursor := invocationCursor{Version: 6, Epoch: s.repository.cursorEpoch(), QueryDigest: searchDigest(q.Filters)}
	if q.Cursor != "" {
		if len(q.Cursor) > 512 {
			return page, ErrInvalidCursor
		}
		raw, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil || len(q.Cursor) > 512 || base64.RawURLEncoding.EncodeToString(raw) != q.Cursor || strictjson.Decode(raw, &cursor, strictjson.Options{MaxBytes: 512, MaxDepth: 2, RejectUnknownMembers: true}) != nil {
			return page, ErrInvalidCursor
		}
		if cursor.Version != 6 || cursor.Epoch != s.repository.cursorEpoch() || cursor.QueryDigest != searchDigest(q.Filters) {
			return page, ErrStaleCursor
		}
		if cursor.UpperSequence <= 0 || cursor.NextSequence <= 0 || cursor.NextSequence > cursor.UpperSequence || !hmac.Equal([]byte(cursor.MAC), []byte(s.repository.cursorMAC(cursor))) {
			return page, ErrInvalidCursor
		}
	}
	page.Items = []contract.GitTrafficRecord{}
	err = s.repository.traffic.view(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var generation string
		var high, pruning int64
		if e := tx.QueryRowContext(ctx, `SELECT generation,high_water,pruning FROM traffic_meta WHERE singleton=1`).Scan(&generation, &high, &pruning); e != nil {
			return e
		}
		if q.Cursor == "" {
			cursor.Generation = generation
			cursor.Pruning = pruning
			cursor.UpperSequence = high
		} else if cursor.Generation != generation || cursor.Pruning != pruning || cursor.UpperSequence > high {
			return ErrStaleCursor
		}
		selected, e := selectGitTraffic(ctx, tx, q, cursor)
		if e != nil || len(selected) == 0 {
			return e
		}
		args := make([]any, len(selected))
		for i, sequence := range selected {
			args[i] = sequence
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
		//nolint:gosec // Only placeholder count is composed; selected sequences are bound.
		rows, e := tx.QueryContext(ctx, gitTrafficSelect+` WHERE insertion_sequence IN (`+placeholders+`) ORDER BY insertion_sequence DESC`, args...)
		if e != nil {
			return e
		}
		var last int64
		for rows.Next() {
			var item contract.GitTrafficRecord
			item, _, e = scanGitTraffic(rows)
			if e != nil {
				break
			}
			if len(page.Items) == q.Limit {
				cursor.NextSequence = last
				cursor.MAC = s.repository.cursorMAC(cursor)
				var raw []byte
				raw, e = json.Marshal(cursor)
				if e != nil {
					break
				}
				encoded := base64.RawURLEncoding.EncodeToString(raw)
				page.NextCursor = &encoded
				break
			}
			page.Items = append(page.Items, item)
			last = item.Sequence
		}
		return errors.Join(e, rows.Err(), rows.Close())
	})
	if err != nil {
		return contract.GitTrafficPage{}, err
	}
	return
}
