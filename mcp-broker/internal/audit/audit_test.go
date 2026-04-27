package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogger_RecordAndQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	rec := Record{
		Timestamp: time.Now(),
		Tool:      "github.get_pr",
		Args:      map[string]any{"repo": "test"},
		Verdict:   "allow",
		Error:     "",
	}
	err = l.Record(context.Background(), rec)
	require.NoError(t, err)

	records, total, err := l.Query(context.Background(), QueryOpts{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, records, 1)
	require.Equal(t, "github.get_pr", records[0].Tool)
}

func TestLogger_QueryWithFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	for _, tool := range []string{"github.get_pr", "github.search", "linear.search"} {
		err := l.Record(context.Background(), Record{
			Timestamp: time.Now(),
			Tool:      tool,
			Verdict:   "allow",
		})
		require.NoError(t, err)
	}

	records, total, err := l.Query(context.Background(), QueryOpts{Tool: "github", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, records, 2)
}

func TestLogger_QueryPagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	for i := range 5 {
		err := l.Record(context.Background(), Record{
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Tool:      "test.tool",
			Verdict:   "allow",
		})
		require.NoError(t, err)
	}

	records, total, err := l.Query(context.Background(), QueryOpts{Limit: 2, Offset: 0})
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, records, 2)

	records2, _, err := l.Query(context.Background(), QueryOpts{Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.Len(t, records2, 2)
}

func TestLogger_RecordWithDenialReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	denied := false
	err = l.Record(context.Background(), Record{
		Timestamp:    time.Now(),
		Tool:         "fs.write",
		Verdict:      "require-approval",
		Approved:     &denied,
		DenialReason: "timeout",
	})
	require.NoError(t, err)

	records, _, err := l.Query(context.Background(), QueryOpts{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, "timeout", records[0].DenialReason)
}

func TestLogger_RecordWithApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	approved := true
	err = l.Record(context.Background(), Record{
		Timestamp: time.Now(),
		Tool:      "fs.write",
		Verdict:   "require-approval",
		Approved:  &approved,
	})
	require.NoError(t, err)

	records, _, err := l.Query(context.Background(), QueryOpts{Limit: 10})
	require.NoError(t, err)
	require.NotNil(t, records[0].Approved)
	require.True(t, *records[0].Approved)
}

func TestLogger_Subscribe_SingleSubscriberReceivesRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	var received []Record
	_ = l.Subscribe(func(rec Record) {
		received = append(received, rec)
	})

	rec := Record{
		Timestamp: time.Now(),
		Tool:      "test.tool",
		Verdict:   "allow",
	}
	require.NoError(t, l.Record(context.Background(), rec))
	require.Len(t, received, 1)
	require.Equal(t, "test.tool", received[0].Tool)
	require.Equal(t, "allow", received[0].Verdict)
}

func TestLogger_Subscribe_MultipleSubscribersEachReceive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	var receivedA, receivedB []Record
	_ = l.Subscribe(func(rec Record) { receivedA = append(receivedA, rec) })
	_ = l.Subscribe(func(rec Record) { receivedB = append(receivedB, rec) })

	rec := Record{
		Timestamp: time.Now(),
		Tool:      "test.tool",
		Verdict:   "allow",
	}
	require.NoError(t, l.Record(context.Background(), rec))
	require.Len(t, receivedA, 1)
	require.Len(t, receivedB, 1)
	require.Equal(t, "test.tool", receivedA[0].Tool)
	require.Equal(t, "test.tool", receivedB[0].Tool)
}

func TestLogger_Subscribe_UnsubscribedDoesNotReceive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	var received []Record
	unsub := l.Subscribe(func(rec Record) {
		received = append(received, rec)
	})

	// First record — subscriber is still active.
	require.NoError(t, l.Record(context.Background(), Record{
		Timestamp: time.Now(),
		Tool:      "before.unsub",
		Verdict:   "allow",
	}))
	require.Len(t, received, 1)

	// Unsubscribe, then insert another record.
	unsub()
	require.NoError(t, l.Record(context.Background(), Record{
		Timestamp: time.Now(),
		Tool:      "after.unsub",
		Verdict:   "allow",
	}))
	// Must still be 1 — no notification for the second record.
	require.Len(t, received, 1)
}

func TestLogger_Subscribe_NoNotificationOnInsertError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)

	var received []Record
	_ = l.Subscribe(func(rec Record) {
		received = append(received, rec)
	})

	// Close the DB to force insert errors.
	_ = l.Close(context.Background())

	insertErr := l.Record(context.Background(), Record{
		Timestamp: time.Now(),
		Tool:      "test.tool",
		Verdict:   "allow",
	})
	require.Error(t, insertErr)
	require.Empty(t, received)
}

func TestLogger_Subscribe_EmptyListDoesNotPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	// No subscribers registered — Record must not panic.
	require.NotPanics(t, func() {
		_ = l.Record(context.Background(), Record{
			Timestamp: time.Now(),
			Tool:      "test.tool",
			Verdict:   "allow",
		})
	})
}

func TestLogger_Subscribe_UnsubscribeTwiceIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	unsub := l.Subscribe(func(_ Record) {})

	// Calling unsubscribe twice must not panic or corrupt state.
	require.NotPanics(t, func() {
		unsub()
		unsub()
	})

	// Logger should still work correctly after double-unsubscribe.
	require.NoError(t, l.Record(context.Background(), Record{
		Timestamp: time.Now(),
		Tool:      "test.tool",
		Verdict:   "allow",
	}))
}
