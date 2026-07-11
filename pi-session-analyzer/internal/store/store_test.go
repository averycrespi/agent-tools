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
		Messages:  []ingest.Message{{ID: "m1", Role: "user", Text: "token=" + secret, SourceLine: 2}},
		ToolCalls: []ingest.ToolCall{{ID: "c1", MessageID: "m1", Name: "bash", Arguments: `{"token":"` + secret + `"}`, SourceLine: 2}},
	}
	meta := SourceMeta{Path: "/sessions/s1.jsonl", Size: 100, ModTimeNS: 200}
	require.NoError(t, s.ReplaceSession(context.Background(), session, meta))
	require.NoError(t, s.ReplaceSession(context.Background(), session, meta))

	var messages int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages))
	require.Equal(t, 1, messages)
	var text, args string
	require.NoError(t, s.db.QueryRow(`SELECT text FROM messages`).Scan(&text))
	require.NoError(t, s.db.QueryRow(`SELECT arguments FROM tool_calls`).Scan(&args))
	require.NotContains(t, text+args, secret)
	require.Contains(t, text+args, "[REDACTED:")

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
