package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/detect"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
	"github.com/stretchr/testify/require"
)

func TestIngestSkipsUnchangedAndRetainsDeletedSources(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	data := `{"type":"session","version":3,"id":"session-one","cwd":"/repo"}` + "\n" + `{"type":"message","id":"a","message":{"role":"assistant","stopReason":"error","content":"failed"}}`
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	db, err := store.Open(filepath.Join(t.TempDir(), "data", "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	service := New(db, detect.Registry())

	first, err := service.Ingest(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, 1, first.Changed)
	require.Equal(t, 0, first.Unchanged)
	findings, err := db.Findings(context.Background(), "session-one")
	require.NoError(t, err)
	require.NotEmpty(t, findings)
	second, err := service.Ingest(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, 0, second.Changed)
	require.Equal(t, 1, second.Unchanged)
	require.NoError(t, os.Remove(path))
	third, err := service.Ingest(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, 0, third.Changed)
	sessions, err := db.ListSessions(context.Background(), 10, "")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
}

func TestDetectRetainsStaleFindingsAfterFailedRerunAndRecovers(t *testing.T) {
	t.Parallel()

	db, err := store.Open(filepath.Join(t.TempDir(), "data", "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ReplaceSession(context.Background(), syntheticSession(), store.SourceMeta{Path: "x", Size: 1, ModTimeNS: 1}))
	good := detect.Detector{Name: "test", Run: func(ingest.Session) ([]detect.Finding, error) {
		return []detect.Finding{{Detector: "test", Classification: detect.Structural, Severity: detect.Warn, Summary: "found", EvidenceID: "e"}}, nil
	}}
	service := New(db, []detect.Detector{good})
	require.NoError(t, service.Detect(context.Background(), "s"))
	bad := detect.Detector{Name: "test", Run: func(ingest.Session) ([]detect.Finding, error) { return nil, assertiveError("boom") }}
	service = New(db, []detect.Detector{bad})
	require.Error(t, service.Detect(context.Background(), "s"))
	rows, err := db.Findings(context.Background(), "s")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Stale)
	require.Equal(t, "failed", rows[0].RunStatus)
	service = New(db, []detect.Detector{good})
	require.NoError(t, service.Detect(context.Background(), "s"))
	rows, err = db.Findings(context.Background(), "s")
	require.NoError(t, err)
	require.False(t, rows[0].Stale)
	require.Equal(t, "success", rows[0].RunStatus)
}

type assertiveError string

func (e assertiveError) Error() string { return string(e) }
func syntheticSession() ingest.Session { return ingest.Session{ID: "s"} }
