package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/require"
)

func TestOpenCreatesSchemaWithWALAndBusyTimeout(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "mailbox.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	var journalMode string
	require.NoError(t, st.db.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journalMode))
	require.Equal(t, "wal", journalMode)
	var busyTimeout int
	require.NoError(t, st.db.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout))
	require.Equal(t, 5000, busyTimeout)

	for _, table := range []string{"messages", "message_events"} {
		var count int
		require.NoError(t, st.db.QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count))
		require.Equal(t, 1, count, table)
	}
}

func TestSendMessageCreatesMessageAndEvent(t *testing.T) {
	st := openTestStore(t)
	msg, created, err := st.SendMessage(context.Background(), SendMessageParams{Sender: "agent", Subject: "Need input", Body: "Please choose", RequiresResponse: true, Severity: SeverityActionRequired})
	require.NoError(t, err)
	require.True(t, created)
	require.NotEmpty(t, msg.ID)
	require.Equal(t, "agent", msg.Sender)
	require.Equal(t, "inbox", msg.Channel)
	require.Equal(t, StatusNew, msg.Status)
	require.True(t, msg.RequiresResponse)
	require.Equal(t, SeverityActionRequired, msg.Severity)
	require.False(t, msg.CreatedAt.IsZero())
	require.False(t, msg.UpdatedAt.IsZero())

	detail, err := st.GetMessage(context.Background(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, msg.ID, detail.Message.ID)
	require.Len(t, detail.Events, 1)
	require.Equal(t, EventMessageCreated, detail.Events[0].Type)
	require.Equal(t, "agent", detail.Events[0].Actor)
	require.JSONEq(t, `{"channel":"inbox","severity":"action_required","requires_response":true}`, string(detail.Events[0].Payload))
}

func TestSendMessageIsIdempotentBySenderAndKey(t *testing.T) {
	st := openTestStore(t)
	first, created, err := st.SendMessage(context.Background(), SendMessageParams{Sender: "agent", Subject: "First", Body: "one", IdempotencyKey: "retry-1"})
	require.NoError(t, err)
	require.True(t, created)
	second, created, err := st.SendMessage(context.Background(), SendMessageParams{Sender: "agent", Subject: "Second", Body: "two", IdempotencyKey: "retry-1"})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, "First", second.Subject)

	messages, err := st.ListMessages(context.Background(), ListMessagesParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, messages.Messages, 1)
}

func TestListMessagesFiltersAndPaginates(t *testing.T) {
	st := openTestStore(t)
	_, _, err := st.SendMessage(context.Background(), SendMessageParams{Sender: "a", Subject: "one", Body: "body", Channel: "ops", Severity: SeverityInfo})
	require.NoError(t, err)
	_, _, err = st.SendMessage(context.Background(), SendMessageParams{Sender: "b", Subject: "two", Body: "body", Channel: "ops", Severity: SeverityError, RequiresResponse: true})
	require.NoError(t, err)
	_, _, err = st.SendMessage(context.Background(), SendMessageParams{Sender: "b", Subject: "three", Body: "body", Channel: "dev", Severity: SeverityError, RequiresResponse: true})
	require.NoError(t, err)

	got, err := st.ListMessages(context.Background(), ListMessagesParams{Channel: "ops", Severity: SeverityError, RequiresResponse: boolPtr(true), Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, got.Limit)
	require.Equal(t, 0, got.Offset)
	require.Equal(t, 1, got.Total)
	require.Equal(t, 1, got.NextOffset)
	require.Len(t, got.Messages, 1)
	require.Equal(t, "two", got.Messages[0].Subject)
	require.Empty(t, got.Messages[0].Body)
}

func TestAckAndResolveAppendEventsAndPersistTransitions(t *testing.T) {
	st := openTestStore(t)
	msg, _, err := st.SendMessage(context.Background(), SendMessageParams{Sender: "agent", Subject: "Need input", Body: "body"})
	require.NoError(t, err)

	acked, changed, err := st.AckMessage(context.Background(), msg.ID, "avery")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, StatusAcknowledged, acked.Status)
	ackedAgain, changed, err := st.AckMessage(context.Background(), msg.ID, "avery")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, StatusAcknowledged, ackedAgain.Status)

	resolved, changed, err := st.ResolveMessageWithResolution(context.Background(), msg.ID, "avery", "done")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, StatusResolved, resolved.Status)
	_, changed, err = st.ResolveMessage(context.Background(), msg.ID, "avery")
	require.NoError(t, err)
	require.False(t, changed)

	detail, err := st.GetMessage(context.Background(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, StatusResolved, detail.Message.Status)
	require.Len(t, detail.Events, 3)
	require.Equal(t, []EventType{EventMessageCreated, EventMessageAcknowledged, EventMessageResolved}, []EventType{detail.Events[0].Type, detail.Events[1].Type, detail.Events[2].Type})
	require.JSONEq(t, `{"resolution":"done"}`, string(detail.Events[2].Payload))
}

func TestValidationErrors(t *testing.T) {
	st := openTestStore(t)
	_, _, err := st.SendMessage(context.Background(), SendMessageParams{Sender: "", Subject: "s", Body: "b"})
	require.ErrorContains(t, err, "sender is required")
	_, _, err = st.SendMessage(context.Background(), SendMessageParams{Sender: "a", Subject: "", Body: "b"})
	require.ErrorContains(t, err, "subject is required")
	_, _, err = st.SendMessage(context.Background(), SendMessageParams{Sender: "a", Subject: "s", Body: "", Severity: Severity("bad")})
	require.ErrorContains(t, err, "body is required")
	_, _, err = st.SendMessage(context.Background(), SendMessageParams{Sender: "a", Subject: "s", Body: "b", Severity: Severity("bad")})
	require.ErrorContains(t, err, "severity")
	_, err = st.ListMessages(context.Background(), ListMessagesParams{Limit: 201})
	require.ErrorContains(t, err, "limit")
	_, err = st.GetMessage(context.Background(), "missing")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "mailbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

func boolPtr(v bool) *bool { return &v }
