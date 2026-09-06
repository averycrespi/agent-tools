package audit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

var (
	ErrInvalidInput    = errors.New("invalid audit input")
	ErrInvalidState    = errors.New("invalid persisted audit history")
	ErrNotFound        = errors.New("audit event not found")
	ErrInvalidCursor   = errors.New("invalid audit cursor")
	ErrStaleCursor     = errors.New("audit cursor is stale")
	ErrHistoryReplaced = errors.New("audit history generation differs")
	ErrUnavailable     = errors.New("audit storage unavailable")
)

type Store interface {
	View(context.Context, func(*sql.Tx) error) error
	Mutate(context.Context, func(*sql.Tx) error) error
	Latched() bool
}

type Repository struct {
	store     Store
	cursorKey [32]byte
}

func NewRepository(store Store) (*Repository, error) {
	if store == nil {
		return nil, ErrInvalidInput
	}
	repository := &Repository{store: store}
	if _, err := rand.Read(repository.cursorKey[:]); err != nil {
		return nil, fmt.Errorf("prepare audit cursors: %w", err)
	}
	return repository, nil
}

// AppendTx never admits another mutation. Its caller must propagate failure so
// the domain change and its evidence roll back together.
func AppendTx(ctx context.Context, tx *sql.Tx, event contract.AuditEvent) (contract.AuditEvent, error) {
	if tx == nil || event.Sequence != "" {
		return contract.AuditEvent{}, ErrInvalidInput
	}
	event.Sequence = "1"
	if contract.ValidateAuditEvent(event) != nil {
		return contract.AuditEvent{}, ErrInvalidInput
	}
	if _, err := historyTx(ctx, tx); err != nil {
		return contract.AuditEvent{}, err
	}
	var previous int64
	var timestamp string
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'control_audit_events'), 0),
		COALESCE((SELECT timestamp FROM control_audit_events ORDER BY insertion_sequence DESC LIMIT 1), '')`).Scan(&previous, &timestamp); err != nil {
		return contract.AuditEvent{}, fmt.Errorf("read audit insertion boundary: %w", err)
	}
	if previous < 0 || previous == math.MaxInt64 || timestamp != "" && !contract.ValidAuditTimestamp(timestamp) {
		return contract.AuditEvent{}, ErrInvalidState
	}
	event.Sequence = strconv.FormatInt(previous+1, 10)
	// Wall-clock rollback must not reorder the append-only chronology.
	if event.Timestamp < timestamp {
		event.Timestamp = timestamp
	}
	contents, err := json.Marshal(event)
	if err != nil || len(contents) > contract.AuditDetailBytes {
		return contract.AuditEvent{}, ErrInvalidInput
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_audit_events (insertion_sequence, event) VALUES (?, ?)`, previous+1, string(contents)); err != nil {
		return contract.AuditEvent{}, fmt.Errorf("append control audit event: %w", err)
	}
	return event, nil
}

// ReplaceHistoryTx changes continuity and records the restore attempt in the
// staged generation together. Installation success must be appended only after
// the replacement has actually been installed.
func ReplaceHistoryTx(ctx context.Context, tx *sql.Tx, attempt contract.AuditEvent) error {
	if tx == nil || attempt.Category != "backup" || attempt.Action != "restore" || attempt.Phase != "attempt" || attempt.Actor.Type != contract.AuditOffline {
		return ErrInvalidInput
	}
	if _, err := historyTx(ctx, tx); err != nil {
		return err
	}
	var generation [32]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return fmt.Errorf("generate audit history generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE control_audit_history SET generation = ? WHERE singleton = 1`, hex.EncodeToString(generation[:])); err != nil {
		return fmt.Errorf("replace audit history generation: %w", err)
	}
	_, err := AppendTx(ctx, tx, attempt)
	return err
}

func (repository *Repository) Append(ctx context.Context, event contract.AuditEvent) (contract.AuditEvent, error) {
	var result contract.AuditEvent
	err := repository.store.Mutate(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = AppendTx(ctx, tx, event)
		return err
	})
	if err != nil {
		return contract.AuditEvent{}, err
	}
	return result, nil
}

func (repository *Repository) Read(ctx context.Context, id, generation string) (contract.AuditItem, error) {
	if !contract.ValidAuditID(id) || generation != "" && !validGeneration(generation) {
		return contract.AuditItem{}, ErrInvalidInput
	}
	var item contract.AuditItem
	err := repository.view(ctx, func(tx *sql.Tx) error {
		var err error
		item.History, err = historyTx(ctx, tx)
		if err != nil {
			return err
		}
		if generation != "" && generation != item.History.Generation {
			return ErrHistoryReplaced
		}
		item.Event, err = scanEvent(tx.QueryRowContext(ctx, `SELECT insertion_sequence, event FROM control_audit_events WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return contract.AuditItem{}, err
	}
	return item, nil
}

func (repository *Repository) view(ctx context.Context, callback func(*sql.Tx) error) error {
	if repository.store.Latched() {
		return ErrUnavailable
	}
	err := repository.store.View(ctx, callback)
	if repository.store.Latched() {
		return ErrUnavailable
	}
	return err
}

func historyTx(ctx context.Context, tx *sql.Tx) (contract.AuditHistory, error) {
	var history contract.AuditHistory
	var pruned int
	if err := tx.QueryRowContext(ctx, `SELECT generation, pruned FROM control_audit_history WHERE singleton = 1`).Scan(&history.Generation, &pruned); err != nil {
		return contract.AuditHistory{}, fmt.Errorf("read audit history: %w", err)
	}
	if !validGeneration(history.Generation) || pruned < 0 || pruned > 1 {
		return contract.AuditHistory{}, ErrInvalidState
	}
	history.Pruned = pruned == 1
	oldest, err := scanEvent(tx.QueryRowContext(ctx, `SELECT insertion_sequence, event FROM control_audit_events ORDER BY insertion_sequence LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		if history.Pruned {
			return contract.AuditHistory{}, ErrInvalidState
		}
		return history, nil
	}
	if err != nil {
		return contract.AuditHistory{}, err
	}
	history.OldestRetained = &contract.AuditBoundary{ID: oldest.ID, Sequence: oldest.Sequence, Timestamp: oldest.Timestamp}
	return history, nil
}

type scanner interface{ Scan(...any) error }

func scanEvent(row scanner) (contract.AuditEvent, error) {
	var sequence int64
	var contents string
	if err := row.Scan(&sequence, &contents); err != nil {
		return contract.AuditEvent{}, err
	}
	var event contract.AuditEvent
	if strictjson.Decode([]byte(contents), &event, strictjson.Options{MaxBytes: contract.AuditDetailBytes, MaxDepth: 8, RejectUnknownMembers: true}) != nil ||
		contract.ValidateAuditEvent(event) != nil || event.Sequence != strconv.FormatInt(sequence, 10) {
		return contract.AuditEvent{}, ErrInvalidState
	}
	canonical, err := json.Marshal(event)
	if err != nil || string(canonical) != contents {
		return contract.AuditEvent{}, ErrInvalidState
	}
	return event, nil
}

func validGeneration(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

// ValidateTx is the bounded semantic boundary used before accepting a stored
// generation, independently of SQLite's structural integrity checks.
func ValidateTx(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return ErrInvalidInput
	}
	history, err := historyTx(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT insertion_sequence, event FROM control_audit_events ORDER BY insertion_sequence LIMIT ?`, contract.AuditRetention+1)
	if err != nil {
		return fmt.Errorf("inspect audit history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	var lastSequence int64
	var lastTimestamp string
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return err
		}
		sequence, _ := strconv.ParseInt(event.Sequence, 10, 64)
		if count > 0 && sequence != lastSequence+1 || event.Timestamp < lastTimestamp {
			return ErrInvalidState
		}
		lastSequence, lastTimestamp = sequence, event.Timestamp
		count++
		if count > contract.AuditRetention {
			return ErrInvalidState
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate audit history: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close audit history: %w", err)
	}
	var allocated int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'control_audit_events'), 0)`).Scan(&allocated); err != nil {
		return fmt.Errorf("read audit sequence: %w", err)
	}
	if lastSequence != allocated || history.Pruned != (allocated > contract.AuditRetention) ||
		!history.Pruned && int64(count) != allocated || history.Pruned && count != contract.AuditRetention {
		return ErrInvalidState
	}
	return nil
}
