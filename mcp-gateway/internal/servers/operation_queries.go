package servers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"slices"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

// OperationQuery opts into globally ordered history; legacy lists are unchanged.
type OperationQuery struct{ Action, Status, Sort, Direction string }

func (q OperationQuery) Validate() bool {
	if q.Action != "" {
		if _, err := contract.ParseServerOperationKind(q.Action); err != nil {
			return false
		}
	}
	if q.Status != "" {
		if _, err := contract.ParseServerOperationState(q.Status); err != nil {
			return false
		}
	}
	return slices.Contains([]string{"", "action", "status", "created", "started", "outcome"}, q.Sort) && (q.Direction == "" || q.Sort != "" && slices.Contains([]string{"ascending", "descending"}, q.Direction))
}

func (q OperationQuery) binding() string {
	contents, _ := json.Marshal(q)
	digest := sha256.Sum256(contents)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

type OperationQueryCursor struct {
	Epoch     string `json:"epoch"`
	Server    string `json:"server"`
	Query     string `json:"query"`
	Upper     int64  `json:"upper"`
	Floor     int64  `json:"floor"`
	Scheduled int64  `json:"scheduled"`
	Running   int64  `json:"running"`
	Position  int    `json:"position"`
}

type OperationQueryPage struct {
	Items []Operation
	Next  *OperationQueryCursor
	contract.CollectionRange
}

// ActiveOperations reads at most three rows, independently of history traversal.
// Two identities and an overflow bit distinguish a sole attachable refresh from
// conflicting work without assuming that nonterminal retention is bounded.
func (repository *Repository) ActiveOperations(ctx context.Context, serverID string) ([]Operation, bool, error) {
	if !validID(serverID) {
		return nil, false, ErrNotFound
	}
	var items []Operation
	err := repository.store.View(ctx, func(tx *sql.Tx) error {
		if err := ensureServerExists(ctx, tx, serverID); err != nil {
			return err
		}
		var err error
		items, err = readOperationRows(ctx, tx, operationSelect+` WHERE server_id = ? AND state IN ('scheduled','running') ORDER BY insertion_sequence, id LIMIT 3`, serverID)
		return err
	})
	more := len(items) > 2
	if more {
		items = items[:2]
	}
	return items, more, mapViewError(err)
}

func readOperationRows(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]Operation, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]Operation, 0)
	for rows.Next() {
		item, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (repository *Repository) QueryOperations(ctx context.Context, serverID string, q OperationQuery, cursor *OperationQueryCursor, limit int) (OperationQueryPage, error) {
	if !validID(serverID) {
		return OperationQueryPage{}, ErrNotFound
	}
	if !q.Validate() || limit < 1 || limit > contract.S2ListPageDefault {
		return OperationQueryPage{}, ErrInvalidInput
	}
	var page OperationQueryPage
	err := repository.store.View(ctx, func(tx *sql.Tx) error {
		if err := ensureServerExists(ctx, tx, serverID); err != nil {
			return err
		}
		snapshot := OperationQueryCursor{Server: serverID, Query: q.binding()}
		if err := tx.QueryRowContext(ctx, `SELECT pruning_generation FROM server_operation_watermarks WHERE server_id = ?`, serverID).Scan(&snapshot.Floor); err != nil {
			return err
		}
		if cursor == nil {
			if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(insertion_sequence),0) FROM server_operations WHERE server_id = ?`, serverID).Scan(&snapshot.Upper); err != nil {
				return err
			}
		} else {
			if cursor.Query != snapshot.Query || cursor.Server != serverID || cursor.Upper < 0 || cursor.Position < 1 || cursor.Floor != snapshot.Floor {
				return ErrStaleCursor
			}
			snapshot.Upper, snapshot.Position = cursor.Upper, cursor.Position
		}
		// Within a fixed insertion watermark, scheduled counts only decrease; if
		// unchanged, running counts only decrease. Every legal mutable transition
		// therefore changes this pair. Terminal rows are immutable, and pruning is
		// fenced separately. No migration, retained row cache, or unbounded Go read
		// is needed to detect reordered or refiltered snapshots.
		if err := tx.QueryRowContext(ctx, `SELECT count(CASE WHEN state = 'scheduled' THEN 1 END), count(CASE WHEN state = 'running' THEN 1 END) FROM server_operations WHERE server_id = ? AND insertion_sequence <= ?`, serverID, snapshot.Upper).Scan(&snapshot.Scheduled, &snapshot.Running); err != nil {
			return err
		}
		if cursor != nil && (cursor.Scheduled != snapshot.Scheduled || cursor.Running != snapshot.Running) {
			return ErrStaleCursor
		}
		where := ` WHERE server_id = ? AND insertion_sequence <= ? AND (? = '' OR kind = ?) AND (? = '' OR state = ?)`
		args := []any{serverID, snapshot.Upper, q.Action, q.Action, q.Status, q.Status}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM server_operations`+where, args...).Scan(&page.TotalCount); err != nil {
			return err
		}
		if cursor != nil && snapshot.Position >= page.TotalCount {
			return ErrStaleCursor
		}
		key, direction := "coalesce(started_at, created_at)", "ASC"
		switch q.Sort {
		case "created":
			key = "created_at"
		case "action":
			key = "kind"
		case "status":
			key = "state"
		case "outcome":
			key = "coalesce(reason, '')"
		}
		if q.Direction == "descending" || q.Sort == "" {
			direction = "DESC"
		}
		args = append(args, limit, snapshot.Position)
		var err error
		page.Items, err = readOperationRows(ctx, tx, operationSelect+where+` ORDER BY `+key+` `+direction+`, id ASC LIMIT ? OFFSET ?`, args...)
		if err != nil {
			return err
		}
		page.Offset = snapshot.Position
		if end := snapshot.Position + len(page.Items); end < page.TotalCount {
			snapshot.Position = end
			page.Next = &snapshot
		}
		return nil
	})
	return page, mapViewError(err)
}
