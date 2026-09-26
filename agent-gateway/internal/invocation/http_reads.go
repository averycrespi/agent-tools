package invocation

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

func validHTTPTrafficFilters(f contract.HTTPTrafficFilters) bool {
	if f.PrincipalID != "" && !validOpaqueInvocationID(f.PrincipalID) {
		return false
	}
	if f.Destination != "" {
		d, err := httppolicy.NewDestination(f.Destination, 443)
		if err != nil || d.Host() != f.Destination {
			return false
		}
	}
	return slices.Contains([]string{"", "request", "connect", "invalid"}, f.Type) && slices.Contains([]string{"", "allow", "block", "intercept", "invalid"}, f.Decision) && slices.Contains([]string{"", "not_dispatched", "outcome_unknown", "succeeded", "prestart_failure", "upstream_failure"}, f.Outcome)
}

func (s *ReadService) GetHTTP(ctx context.Context, id string) (result contract.HTTPTrafficRecord, err error) {
	if !validOpaqueInvocationID(id) {
		return result, ErrInvalidInput
	}
	if s.repository.traffic == nil {
		return result, ErrInvalidState
	}
	err = s.repository.traffic.view(ctx, func(tx *sql.Tx) error {
		var err error
		result, _, err = scanHTTPTraffic(tx.QueryRowContext(ctx, httpTrafficSelect+` WHERE id=?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return contract.HTTPTrafficRecord{}, err
	}
	return result, nil
}

func (s *ReadService) ListHTTP(ctx context.Context, q contract.HTTPTrafficQuery) (page contract.HTTPTrafficPage, err error) {
	if q.Limit < 1 || q.Limit > 256 || !validHTTPTrafficFilters(q.Filters) {
		return page, ErrInvalidInput
	}
	if s.repository.traffic == nil {
		return page, ErrInvalidState
	}
	cursor := invocationCursor{Version: 4, Epoch: s.repository.cursorEpoch(), QueryDigest: searchDigest(q.Filters)}
	if q.Cursor != "" {
		if len(q.Cursor) > 512 {
			return page, ErrInvalidCursor
		}
		raw, decodeErr := base64.RawURLEncoding.DecodeString(q.Cursor)
		if decodeErr != nil || base64.RawURLEncoding.EncodeToString(raw) != q.Cursor || strictjson.Decode(raw, &cursor, strictjson.Options{MaxBytes: 512, MaxDepth: 2, RejectUnknownMembers: true}) != nil {
			return page, ErrInvalidCursor
		}
		if cursor.Version != 4 || cursor.Epoch != s.repository.cursorEpoch() {
			return page, ErrStaleCursor
		}
		if cursor.UpperSequence <= 0 || cursor.NextSequence <= 0 || cursor.NextSequence > cursor.UpperSequence || !hmac.Equal([]byte(cursor.MAC), []byte(s.repository.cursorMAC(cursor))) {
			return page, ErrInvalidCursor
		}
		if cursor.QueryDigest != searchDigest(q.Filters) {
			return page, ErrStaleCursor
		}
	}
	page.Items = []contract.HTTPTrafficSummary{}
	err = s.repository.traffic.view(ctx, func(tx *sql.Tx) error {
		var generation string
		var high, pruning int64
		if err := tx.QueryRowContext(ctx, `SELECT generation,high_water,pruning FROM traffic_meta WHERE singleton=1`).Scan(&generation, &high, &pruning); err != nil {
			return err
		}
		if q.Cursor == "" {
			cursor.Generation = generation
			cursor.Pruning = pruning
			cursor.UpperSequence = high
		} else if cursor.Generation != generation || cursor.Pruning != pruning || cursor.UpperSequence > high {
			return ErrStaleCursor
		}
		clauses := []string{"insertion_sequence<=?"}
		args := []any{cursor.UpperSequence}
		if q.Cursor != "" {
			clauses = append(clauses, "insertion_sequence<?")
			args = append(args, cursor.NextSequence)
		}
		for _, filter := range []struct{ column, value string }{{"principal_id", q.Filters.PrincipalID}, {"destination", q.Filters.Destination}, {"traffic_type", q.Filters.Type}, {"decision", q.Filters.Decision}, {"outcome", q.Filters.Outcome}} {
			if filter.value != "" {
				clauses = append(clauses, filter.column+"=?")
				args = append(args, filter.value)
			}
		}
		args = append(args, q.Limit+1)
		//nolint:gosec // Predicate columns are fixed above; every filter value is bound.
		rows, err := tx.QueryContext(ctx, `SELECT insertion_sequence,id,json_extract(admission,'$.admitted_at'),principal_id,json_extract(admission,'$.target'),traffic_type,decision,outcome,json_extract(admission,'$.rejection'),json_extract(admission,'$.connect'),CASE WHEN json_type(admission,'$.rejection')='object' THEN 'gateway' ELSE coalesce(json_extract(completion,'$.response_source'),'') END FROM http_traffic WHERE `+strings.Join(clauses, " AND ")+` ORDER BY insertion_sequence DESC LIMIT ?`, args...)
		if err != nil {
			return err
		}
		var last int64
		for rows.Next() {
			var item contract.HTTPTrafficSummary
			var target, rejection, connect sql.NullString
			var sequence int64
			if err = rows.Scan(&sequence, &item.ID, &item.AdmittedAt, &item.PrincipalID, &target, &item.Type, &item.Decision, &item.Outcome, &rejection, &connect, &item.ResponseSource); err != nil {
				break
			}
			if len(page.Items) == q.Limit {
				cursor.NextSequence = last
				cursor.MAC = s.repository.cursorMAC(cursor)
				raw, encodeErr := json.Marshal(cursor)
				if encodeErr != nil {
					err = ErrInvalidCursor
					break
				}
				encoded := base64.RawURLEncoding.EncodeToString(raw)
				if len(encoded) > 512 {
					err = ErrInvalidCursor
					break
				}
				page.NextCursor = &encoded
				break
			}
			if target.Valid {
				item.Target = &contract.HTTPTrafficTarget{}
				if strictjson.Decode([]byte(target.String), item.Target, strictjson.Options{MaxBytes: 512, MaxDepth: 2, RejectUnknownMembers: true}) != nil {
					err = ErrInvalidState
					break
				}
			}
			if rejection.Valid {
				item.Rejection = &contract.HTTPRejection{}
				if strictjson.Decode([]byte(rejection.String), item.Rejection, strictjson.Options{MaxBytes: 128, MaxDepth: 2, RejectUnknownMembers: true}) != nil || !item.Rejection.Valid() || item.Type != "invalid" {
					err = ErrInvalidState
					break
				}
			}
			if connect.Valid {
				item.Connect = &contract.HTTPConnectContext{}
				if strictjson.Decode([]byte(connect.String), item.Connect, strictjson.Options{MaxBytes: 512, MaxDepth: 2, RejectUnknownMembers: true}) != nil {
					err = ErrInvalidState
					break
				}
			}
			page.Items = append(page.Items, item)
			last = sequence
		}
		return errors.Join(err, rows.Err(), rows.Close())
	})
	if err != nil {
		return contract.HTTPTrafficPage{}, err
	}
	return page, nil
}
