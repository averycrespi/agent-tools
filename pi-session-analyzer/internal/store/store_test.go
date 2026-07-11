package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestOpenAndReplaceSessionArePrivateScrubbedAndIdempotent(t *testing.T) {
	t.Parallel()

	ancestor := t.TempDir()
	require.NoError(t, os.Chmod(ancestor, 0o755))
	leaf := filepath.Join(ancestor, "pi-session-analyzer")
	dbPath := filepath.Join(leaf, "sessions.db")

	s, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	secret := "ghp_abcdefghijklmnopqrstuvwxyz1234567890"
	session := ingest.Session{
		ID: "s1", CWD: "/repo", Stats: ingest.ParseStats{Total: 2},
		Messages:       []ingest.Message{{ID: "m1", Role: "user", Text: "token=" + secret, SourceLine: 2, OutputTokens: 3, Cost: 0.2}},
		ToolCalls:      []ingest.ToolCall{{ID: "c1", MessageID: "m1", Name: "bash", Arguments: `{"token":"` + secret + `"}`, SourceLine: 2}},
		ToolResults:    []ingest.ToolResult{{ID: "r1", CallID: "c1", Content: "password=abc", SourceLine: 3}},
		Events:         []ingest.Event{{ID: "e1", Type: "compaction", Value: "secret=" + secret, Details: `{"password":"abc","thinking":"private","thinkingSignature":"signature"}`, SourceLine: 4}},
		CustomStates:   []ingest.CustomState{{ID: "s1", Type: "goal-state", Data: `{"token":"` + secret + `"}`, SourceLine: 5}},
		CustomMessages: []ingest.CustomMessage{{ID: "cm1", Type: "broker-guard", Content: "password=abc", Details: `{"secret":"abc"}`, SourceLine: 6}},
	}
	meta := SourceMeta{Path: "/sessions/s1.jsonl", Size: 100, ModTimeNS: 200}
	require.NoError(t, s.ReplaceSession(context.Background(), session, meta))
	require.NoError(t, s.ReplaceSession(context.Background(), session, meta))

	for _, table := range []string{"messages", "tool_calls", "tool_results", "events", "custom_state", "custom_messages"} {
		var count int
		require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count)) //nolint:gosec // closed test table list
		require.Equal(t, 1, count, table)
	}
	summary, err := s.SessionSummary(context.Background(), "s1")
	require.NoError(t, err)
	require.Equal(t, int64(3), summary.OutputTokens)
	require.InDelta(t, 0.2, summary.Cost, 0.0001)
	finding := DetectorFinding{Detector: "test", Classification: "structural", Severity: "warn", Summary: "test", EvidenceID: "e1", Details: `{}`}
	require.NoError(t, s.SaveDetectorSuccess(context.Background(), "s1", "test", []DetectorFinding{finding}))
	require.NoError(t, s.SaveDetectorSuccess(context.Background(), "s1", "test", []DetectorFinding{finding}))
	var findingCount int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&findingCount))
	require.Equal(t, 1, findingCount)
	var text, args string
	require.NoError(t, s.db.QueryRow(`SELECT text FROM messages`).Scan(&text))
	require.NoError(t, s.db.QueryRow(`SELECT arguments FROM tool_calls`).Scan(&args))
	require.NotContains(t, text+args, secret)
	require.Contains(t, text+args, "[REDACTED:")
	for _, query := range []string{
		`SELECT content FROM tool_results`, `SELECT value || details FROM events`,
		`SELECT data FROM custom_state`, `SELECT content || details FROM custom_messages`,
	} {
		var persisted string
		require.NoError(t, s.db.QueryRow(query).Scan(&persisted))
		require.NotContains(t, persisted, secret)
		require.NotContains(t, persisted, "abc")
		require.NotContains(t, persisted, "private")
		require.NotContains(t, persisted, "signature")
		require.Contains(t, persisted, "[REDACTED:")
	}

	leafInfo, err := os.Stat(leaf)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), leafInfo.Mode().Perm())
	ancestorInfo, err := os.Stat(ancestor)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), ancestorInfo.Mode().Perm())
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), path)
	}
}

func TestOpenCreatesMissingAncestorsWithoutMakingThemPrivate(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	ancestor := filepath.Join(base, "shared")
	leaf := filepath.Join(ancestor, "pi-session-analyzer")
	db, err := Open(filepath.Join(leaf, "sessions.db"))
	require.NoError(t, err)
	require.NoError(t, db.Close())
	ancestorInfo, err := os.Stat(ancestor)
	require.NoError(t, err)
	require.NotEqual(t, os.FileMode(0o700), ancestorInfo.Mode().Perm())
	leafInfo, err := os.Stat(leaf)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), leafInfo.Mode().Perm())
}

func TestOpenDoesNotChmodExistingSharedParent(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o755))
	db, err := Open(filepath.Join(parent, "sessions.db"))
	require.NoError(t, err)
	require.NoError(t, db.Close())
	info, err := os.Stat(parent)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

func TestReopenRepairsRecreatedSidecarsAndSchemaHasNoRawTier(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "leaf", "sessions.db")
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		_ = os.Remove(sidecar)
	}
	db, err = Open(path)
	require.NoError(t, err)
	require.NoError(t, db.ReplaceSession(context.Background(), ingest.Session{ID: "s"}, SourceMeta{Path: "x", Size: 1, ModTimeNS: 1}))
	defer func() { require.NoError(t, db.Close()) }()
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		info, statErr := os.Stat(file)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	var rawTables int
	require.NoError(t, db.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND lower(name) LIKE '%raw%'`).Scan(&rawTables))
	require.Zero(t, rawTables)
}

func TestTopFailuresFiltersOrdersAndLimitsInQuery(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "leaf", "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "s"}, SourceMeta{Path: "x", Size: 1, ModTimeNS: 1}))
	findings := []DetectorFinding{
		{Detector: "test", Classification: "structural", Severity: "info", Summary: "info", EvidenceID: "a", Details: `{}`},
		{Detector: "test", Classification: "structural", Severity: "warn", Summary: "warn", EvidenceID: "b", Details: `{}`},
		{Detector: "test", Classification: "structural", Severity: "error", Summary: "error", EvidenceID: "c", Details: `{}`},
	}
	require.NoError(t, s.SaveDetectorSuccess(context.Background(), "s", "test", findings))
	rows, err := s.TopFailures(context.Background(), FailureQuery{Limit: 1, Classification: "structural", MinSeverity: "warn"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "error", rows[0].Severity)
}

func TestSourceMetadataAndSessionRemainWithoutSource(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "leaf", "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	meta := SourceMeta{Path: "/gone.jsonl", Size: 1, ModTimeNS: 2}
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "kept"}, meta))

	unchanged, err := s.SourceUnchanged(context.Background(), meta)
	require.NoError(t, err)
	require.True(t, unchanged)
	sessions, err := s.ListSessions(context.Background(), 10, "")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "kept", sessions[0].ID)
}
