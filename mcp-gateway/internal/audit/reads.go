package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type Query = contract.AuditListQuery

type cursor struct {
	Version    int    `json:"v"`
	Generation string `json:"g"`
	Epoch      string `json:"e"`
	Filters    string `json:"f"`
	Oldest     int64  `json:"o"`
	Upper      int64  `json:"u"`
	Before     int64  `json:"b"`
}

func (repository *Repository) List(ctx context.Context, query Query) (contract.AuditPage, error) {
	if query.Limit < 1 || query.Limit > contract.AuditPageLimit || contract.ValidateAuditFilters(query.Filters) != nil ||
		query.Generation != "" && !validGeneration(query.Generation) {
		return contract.AuditPage{}, ErrInvalidInput
	}
	var binding cursor
	if query.Cursor != "" {
		var err error
		binding, err = repository.decodeCursor(query.Cursor)
		if err != nil {
			return contract.AuditPage{}, err
		}
		if binding.Filters != filterHash(query.Filters) {
			return contract.AuditPage{}, ErrInvalidCursor
		}
	}
	page := contract.AuditPage{Items: []contract.AuditSummary{}}
	err := repository.view(ctx, func(tx *sql.Tx) error {
		var err error
		page.History, err = historyTx(ctx, tx)
		if err != nil {
			return err
		}
		if query.Generation != "" && query.Generation != page.History.Generation || query.Cursor != "" && binding.Generation != page.History.Generation {
			return ErrHistoryReplaced
		}
		var oldest int64
		if page.History.OldestRetained != nil {
			oldest, _ = strconv.ParseInt(page.History.OldestRetained.Sequence, 10, 64)
		}
		if query.Cursor != "" {
			if oldest != binding.Oldest {
				return ErrStaleCursor
			}
		} else {
			binding = cursor{Version: 1, Generation: page.History.Generation, Epoch: repository.epoch(), Filters: filterHash(query.Filters), Oldest: oldest}
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(insertion_sequence), 0) FROM control_audit_events`).Scan(&binding.Upper); err != nil {
				return err
			}
		}
		statement, args := listStatement(query.Filters, binding, query.Limit+1)
		rows, err := tx.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			event, err := scanEvent(rows)
			if err != nil {
				return err
			}
			if len(page.Items) == query.Limit {
				binding.Before, _ = strconv.ParseInt(page.Items[len(page.Items)-1].Sequence, 10, 64)
				encoded, err := repository.encodeCursor(binding)
				if err != nil {
					return err
				}
				page.NextCursor = &encoded
				break
			}
			page.Items = append(page.Items, event.AuditSummary)
		}
		return rows.Err()
	})
	if err != nil {
		return contract.AuditPage{}, err
	}
	return page, nil
}

func listStatement(filters contract.AuditFilters, binding cursor, limit int) (string, []any) {
	parts := []string{"insertion_sequence <= ?"}
	args := []any{binding.Upper}
	if binding.Before > 0 {
		parts = append(parts, "insertion_sequence < ?")
		args = append(args, binding.Before)
	}
	for _, filter := range []struct{ column, value string }{
		{"actor_type", string(filters.ActorType)}, {"category", filters.Category}, {"action", filters.Action},
		{"target_type", filters.TargetType}, {"target_id", filters.TargetID}, {"outcome", filters.Outcome}, {"correlation_id", filters.CorrelationID},
	} {
		if filter.value != "" {
			parts = append(parts, filter.column+" = ?")
			args = append(args, filter.value)
		}
	}
	if filters.CredentialID != "" {
		parts = append(parts, "(credential_id = ? OR initiator_id = ?)")
		args = append(args, filters.CredentialID, filters.CredentialID)
	}
	if filters.From != "" {
		parts = append(parts, "timestamp >= ? AND timestamp < ?")
		args = append(args, filters.From, filters.Until)
	}
	args = append(args, limit)
	return "SELECT insertion_sequence, event FROM control_audit_events WHERE " + strings.Join(parts, " AND ") + " ORDER BY insertion_sequence DESC LIMIT ?", args
}

func filterHash(filters contract.AuditFilters) string {
	contents, _ := json.Marshal(filters)
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func (repository *Repository) epoch() string {
	digest := sha256.Sum256(repository.cursorKey[:])
	return hex.EncodeToString(digest[:16])
}

func (repository *Repository) encodeCursor(value cursor) (string, error) {
	contents, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, repository.cursorKey[:])
	_, _ = mac.Write(contents)
	encoded := base64.RawURLEncoding.EncodeToString(contents) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(encoded) > contract.AuditCursorBytes {
		return "", ErrInvalidCursor
	}
	return encoded, nil
}

func (repository *Repository) decodeCursor(encoded string) (cursor, error) {
	if encoded == "" || len(encoded) > contract.AuditCursorBytes {
		return cursor{}, ErrInvalidCursor
	}
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return cursor{}, ErrInvalidCursor
	}
	contents, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(contents) != parts[0] {
		return cursor{}, ErrInvalidCursor
	}
	macBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(macBytes) != sha256.Size || base64.RawURLEncoding.EncodeToString(macBytes) != parts[1] {
		return cursor{}, ErrInvalidCursor
	}
	var value cursor
	if strictjson.Decode(contents, &value, strictjson.Options{MaxBytes: contract.AuditCursorBytes, MaxDepth: 2, RejectUnknownMembers: true}) != nil ||
		value.Version != 1 || !validGeneration(value.Generation) || !validGeneration(value.Filters) ||
		value.Oldest < 1 || value.Upper < value.Oldest || value.Before <= value.Oldest || value.Before > value.Upper {
		return cursor{}, ErrInvalidCursor
	}
	if value.Epoch != repository.epoch() {
		return cursor{}, ErrStaleCursor
	}
	mac := hmac.New(sha256.New, repository.cursorKey[:])
	_, _ = mac.Write(contents)
	if !hmac.Equal(macBytes, mac.Sum(nil)) {
		return cursor{}, ErrInvalidCursor
	}
	return value, nil
}
