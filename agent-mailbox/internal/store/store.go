package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type Status string

type Severity string

type EventType string

const (
	StatusNew          Status = "new"
	StatusAcknowledged Status = "acknowledged"
	StatusResolved     Status = "resolved"

	SeverityInfo           Severity = "info"
	SeveritySuccess        Severity = "success"
	SeverityWarning        Severity = "warning"
	SeverityError          Severity = "error"
	SeverityActionRequired Severity = "action_required"

	EventMessageCreated      EventType = "message.created"
	EventMessageAcknowledged EventType = "message.acknowledged"
	EventMessageResolved     EventType = "message.resolved"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

type Message struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Sender           string    `json:"sender"`
	Channel          string    `json:"channel"`
	ThreadID         string    `json:"thread_id,omitempty"`
	Subject          string    `json:"subject"`
	Body             string    `json:"body,omitempty"`
	Severity         Severity  `json:"severity"`
	RequiresResponse bool      `json:"requires_response"`
	Status           Status    `json:"status"`
	IdempotencyKey   string    `json:"idempotency_key,omitempty"`
}

type Event struct {
	ID        int64           `json:"id"`
	MessageID string          `json:"message_id"`
	Type      EventType       `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Actor     string          `json:"actor"`
	Payload   json.RawMessage `json:"payload"`
}

type MessageDetail struct {
	Message Message `json:"message"`
	Events  []Event `json:"events"`
}

type SendMessageParams struct {
	Sender           string
	Subject          string
	Body             string
	Channel          string
	ThreadID         string
	Severity         Severity
	RequiresResponse bool
	IdempotencyKey   string
}

type ListMessagesParams struct {
	Status           Status
	Channel          string
	Sender           string
	Severity         Severity
	RequiresResponse *bool
	Limit            int
	Offset           int
}

type ListMessagesResult struct {
	Messages   []Message `json:"messages"`
	Limit      int       `json:"limit"`
	Offset     int       `json:"offset"`
	NextOffset int       `json:"next_offset"`
	Total      int       `json:"total"`
}

type Store struct {
	db *sql.DB
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    sender TEXT NOT NULL,
    channel TEXT NOT NULL,
    thread_id TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    severity TEXT NOT NULL,
    requires_response INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_sender_idempotency ON messages(sender, idempotency_key) WHERE idempotency_key <> '';
CREATE INDEX IF NOT EXISTS idx_messages_status_updated ON messages(status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_channel ON messages(channel);
CREATE INDEX IF NOT EXISTS idx_messages_sender ON messages(sender);
CREATE INDEX IF NOT EXISTS idx_messages_severity ON messages(severity);

CREATE TABLE IF NOT EXISTS message_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL REFERENCES messages(id),
    type TEXT NOT NULL,
    created_at TEXT NOT NULL,
    actor TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_message_events_message_id ON message_events(message_id, id);
`

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	if err := ensurePrivateFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := restrictStoreFiles(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func ensurePrivateFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // The DB path is the documented user-controlled --db-path/store path.
	if err != nil {
		return fmt.Errorf("create store file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close store file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict store file: %w", err)
	}
	return nil
}

func restrictStoreFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restrict store file %s: %w", candidate, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SendMessage(ctx context.Context, p SendMessageParams) (Message, bool, error) {
	p = normalizeSend(p)
	if err := validateSend(p); err != nil {
		return Message{}, false, err
	}
	if p.IdempotencyKey != "" {
		msg, err := s.getByIdempotency(ctx, p.Sender, p.IdempotencyKey)
		if err == nil {
			return msg, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Message{}, false, err
		}
	}
	id, err := newID()
	if err != nil {
		return Message{}, false, err
	}
	now := time.Now().UTC().Round(0)
	msg := Message{ID: id, CreatedAt: now, UpdatedAt: now, Sender: p.Sender, Channel: p.Channel, ThreadID: p.ThreadID, Subject: p.Subject, Body: p.Body, Severity: p.Severity, RequiresResponse: p.RequiresResponse, Status: StatusNew, IdempotencyKey: p.IdempotencyKey}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, err
	}
	defer tx.Rollback() //nolint:errcheck
	_, err = tx.ExecContext(ctx, `INSERT INTO messages (id, created_at, updated_at, sender, channel, thread_id, subject, body, severity, requires_response, status, idempotency_key) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, msg.ID, formatTime(msg.CreatedAt), formatTime(msg.UpdatedAt), msg.Sender, msg.Channel, msg.ThreadID, msg.Subject, msg.Body, msg.Severity, boolInt(msg.RequiresResponse), msg.Status, msg.IdempotencyKey)
	if err != nil {
		if p.IdempotencyKey != "" {
			got, getErr := s.getByIdempotency(ctx, p.Sender, p.IdempotencyKey)
			if getErr == nil {
				return got, false, nil
			}
		}
		return Message{}, false, err
	}
	payload := map[string]any{"channel": msg.Channel, "severity": msg.Severity, "requires_response": msg.RequiresResponse}
	if msg.ThreadID != "" {
		payload["thread_id"] = msg.ThreadID
	}
	if msg.IdempotencyKey != "" {
		payload["idempotency_key"] = msg.IdempotencyKey
	}
	if err := insertEvent(ctx, tx, msg.ID, EventMessageCreated, msg.CreatedAt, msg.Sender, payload); err != nil {
		return Message{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, false, err
	}
	return msg, true, nil
}

func (s *Store) ListMessages(ctx context.Context, p ListMessagesParams) (ListMessagesResult, error) {
	p = normalizeList(p)
	if err := validateList(p); err != nil {
		return ListMessagesResult{}, err
	}
	where, args := listWhere(p)
	countQuery := `SELECT count(*) FROM messages` + where
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return ListMessagesResult{}, err
	}
	queryArgs := append(append([]any{}, args...), p.Limit, p.Offset)
	//nolint:gosec // WHERE clauses are assembled only from fixed column names with parameter placeholders.
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at, updated_at, sender, channel, thread_id, subject, '' as body, severity, requires_response, status, idempotency_key FROM messages`+where+` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return ListMessagesResult{}, err
	}
	defer rows.Close() //nolint:errcheck
	messages := []Message{}
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return ListMessagesResult{}, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return ListMessagesResult{}, err
	}
	return ListMessagesResult{Messages: messages, Limit: p.Limit, Offset: p.Offset, NextOffset: p.Offset + len(messages), Total: total}, nil
}

func (s *Store) GetMessage(ctx context.Context, id string) (MessageDetail, error) {
	msg, err := s.getMessage(ctx, id)
	if err != nil {
		return MessageDetail{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, message_id, type, created_at, actor, payload FROM message_events WHERE message_id = ? ORDER BY id ASC`, id)
	if err != nil {
		return MessageDetail{}, err
	}
	defer rows.Close() //nolint:errcheck
	events := []Event{}
	for rows.Next() {
		var event Event
		var created string
		var payload string
		if err := rows.Scan(&event.ID, &event.MessageID, &event.Type, &created, &event.Actor, &payload); err != nil {
			return MessageDetail{}, err
		}
		event.Payload = json.RawMessage(payload)
		event.CreatedAt, err = parseTime(created)
		if err != nil {
			return MessageDetail{}, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return MessageDetail{}, err
	}
	return MessageDetail{Message: msg, Events: events}, nil
}

func (s *Store) AckMessage(ctx context.Context, id, actor string) (Message, bool, error) {
	return s.transition(ctx, id, actorOrDefault(actor), StatusAcknowledged, EventMessageAcknowledged, false, map[string]any{})
}

func (s *Store) ResolveMessage(ctx context.Context, id, actor string) (Message, bool, error) {
	return s.ResolveMessageWithResolution(ctx, id, actor, "")
}

func (s *Store) ResolveMessageWithResolution(ctx context.Context, id, actor, resolution string) (Message, bool, error) {
	payload := map[string]any{}
	if strings.TrimSpace(resolution) != "" {
		payload["resolution"] = strings.TrimSpace(resolution)
	}
	return s.transition(ctx, id, actorOrDefault(actor), StatusResolved, EventMessageResolved, true, payload)
}

func (s *Store) transition(ctx context.Context, id, actor string, target Status, eventType EventType, allowFromAck bool, payload any) (Message, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, err
	}
	defer tx.Rollback() //nolint:errcheck
	msg, err := getMessageTx(ctx, tx, id)
	if err != nil {
		return Message{}, false, err
	}
	if msg.Status == target || msg.Status == StatusResolved {
		return msg, false, tx.Commit()
	}
	if target == StatusAcknowledged && msg.Status != StatusNew {
		return msg, false, tx.Commit()
	}
	if target == StatusResolved && msg.Status != StatusNew && (!allowFromAck || msg.Status != StatusAcknowledged) {
		return msg, false, tx.Commit()
	}
	now := time.Now().UTC().Round(0)
	_, err = tx.ExecContext(ctx, `UPDATE messages SET status = ?, updated_at = ? WHERE id = ?`, target, formatTime(now), id)
	if err != nil {
		return Message{}, false, err
	}
	if err := insertEvent(ctx, tx, id, eventType, now, actor, payload); err != nil {
		return Message{}, false, err
	}
	msg.Status = target
	msg.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return Message{}, false, err
	}
	return msg, true, nil
}

func (s *Store) getByIdempotency(ctx context.Context, sender, key string) (Message, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, created_at, updated_at, sender, channel, thread_id, subject, body, severity, requires_response, status, idempotency_key FROM messages WHERE sender = ? AND idempotency_key = ?`, sender, key)
	return scanMessage(row)
}

func (s *Store) getMessage(ctx context.Context, id string) (Message, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, created_at, updated_at, sender, channel, thread_id, subject, body, severity, requires_response, status, idempotency_key FROM messages WHERE id = ?`, id)
	return scanMessage(row)
}

func getMessageTx(ctx context.Context, tx *sql.Tx, id string) (Message, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, created_at, updated_at, sender, channel, thread_id, subject, body, severity, requires_response, status, idempotency_key FROM messages WHERE id = ?`, id)
	return scanMessage(row)
}

type scanner interface{ Scan(dest ...any) error }

func scanMessage(row scanner) (Message, error) {
	var msg Message
	var created, updated string
	var requiresResponse int
	if err := row.Scan(&msg.ID, &created, &updated, &msg.Sender, &msg.Channel, &msg.ThreadID, &msg.Subject, &msg.Body, &msg.Severity, &requiresResponse, &msg.Status, &msg.IdempotencyKey); err != nil {
		return Message{}, err
	}
	var err error
	msg.CreatedAt, err = parseTime(created)
	if err != nil {
		return Message{}, err
	}
	msg.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return Message{}, err
	}
	msg.RequiresResponse = requiresResponse != 0
	return msg, nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, messageID string, typ EventType, at time.Time, actor string, payload any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO message_events (message_id, type, created_at, actor, payload) VALUES (?, ?, ?, ?, ?)`, messageID, typ, formatTime(at), actor, string(payloadJSON))
	return err
}

func normalizeSend(p SendMessageParams) SendMessageParams {
	p.Sender = strings.TrimSpace(p.Sender)
	p.Subject = strings.TrimSpace(p.Subject)
	if p.Channel = strings.TrimSpace(p.Channel); p.Channel == "" {
		p.Channel = "inbox"
	}
	p.ThreadID = strings.TrimSpace(p.ThreadID)
	if p.Severity == "" {
		p.Severity = SeverityInfo
	}
	return p
}

func validateSend(p SendMessageParams) error {
	if p.Sender == "" {
		return fmt.Errorf("sender is required")
	}
	if p.Subject == "" {
		return fmt.Errorf("subject is required")
	}
	if p.Body == "" {
		return fmt.Errorf("body is required")
	}
	if !validSeverity(p.Severity) {
		return fmt.Errorf("severity must be one of info, success, warning, error, action_required")
	}
	return nil
}

func normalizeList(p ListMessagesParams) ListMessagesParams {
	if p.Limit == 0 {
		p.Limit = DefaultLimit
	}
	return p
}

func validateList(p ListMessagesParams) error {
	if p.Status != "" && !validStatus(p.Status) {
		return fmt.Errorf("status must be one of new, acknowledged, resolved")
	}
	if p.Severity != "" && !validSeverity(p.Severity) {
		return fmt.Errorf("severity must be one of info, success, warning, error, action_required")
	}
	if p.Limit < 1 || p.Limit > MaxLimit {
		return fmt.Errorf("limit must be between 1 and %d", MaxLimit)
	}
	if p.Offset < 0 {
		return fmt.Errorf("offset must be non-negative")
	}
	return nil
}

func listWhere(p ListMessagesParams) (string, []any) {
	clauses := []string{}
	args := []any{}
	add := func(clause string, arg any) {
		clauses = append(clauses, clause)
		args = append(args, arg)
	}
	if p.Status != "" {
		add("status = ?", p.Status)
	}
	if p.Channel != "" {
		add("channel = ?", p.Channel)
	}
	if p.Sender != "" {
		add("sender = ?", p.Sender)
	}
	if p.Severity != "" {
		add("severity = ?", p.Severity)
	}
	if p.RequiresResponse != nil {
		add("requires_response = ?", boolInt(*p.RequiresResponse))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func validStatus(status Status) bool {
	switch status {
	case StatusNew, StatusAcknowledged, StatusResolved:
		return true
	default:
		return false
	}
}

func validSeverity(severity Severity) bool {
	switch severity {
	case SeverityInfo, SeveritySuccess, SeverityWarning, SeverityError, SeverityActionRequired:
		return true
	default:
		return false
	}
}

func actorOrDefault(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "user"
	}
	return actor
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate message id: %w", err)
	}
	return "msg_" + hex.EncodeToString(b[:]), nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
