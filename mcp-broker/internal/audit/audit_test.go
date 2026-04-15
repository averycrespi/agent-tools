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

func TestRecordWithGrant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l, err := NewLogger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close(context.Background()) }()

	err = l.Record(context.Background(), Record{
		Timestamp:    time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
		Tool:         "git.git_push",
		Args:         map[string]any{"branch": "feat/foo"},
		Verdict:      "allow",
		GrantID:      "grt_abc",
		GrantOutcome: "matched",
	})
	require.NoError(t, err)

	records, _, err := l.Query(context.Background(), QueryOpts{Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "grt_abc", records[0].GrantID)
	require.Equal(t, "matched", records[0].GrantOutcome)
}

func TestAuditSchemaIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	l1, err := NewLogger(path)
	require.NoError(t, err)
	require.NoError(t, l1.Close(context.Background()))
	l2, err := NewLogger(path)
	require.NoError(t, err)
	require.NoError(t, l2.Close(context.Background()))
}
