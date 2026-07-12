package dashboard

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRunCanceledContextPrintsLoopbackURLAndStops(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	ctx, cancel := context.WithCancel(context.Background())
	var output bytes.Buffer
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, path, Options{Port: 0, NoOpen: true, Output: &output, Ready: func(url string) { ready <- url }})
	}()
	<-ready
	cancel()
	require.NoError(t, <-done)
	require.True(t, strings.HasPrefix(output.String(), "http://127.0.0.1:"))
}

func TestRunBrowserFailureIsNonfatalAfterPrintingURL(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	ctx, cancel := context.WithCancel(context.Background())
	var output bytes.Buffer
	opened := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, path, Options{
			Output: &output,
			OpenBrowser: func(_ context.Context, url string) error {
				opened <- url
				return errors.New("no browser")
			},
		})
	}()
	url := <-opened
	cancel()
	require.NoError(t, <-done)
	require.Contains(t, output.String(), url)
	require.Contains(t, output.String(), "browser open failed")
}

func TestRunRejectsPreDashboardSchemaWithIngestHint(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, timestamp TEXT NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = Run(ctx, path, Options{NoOpen: true})
	require.ErrorContains(t, err, "run pi-session-analyzer ingest")
}

func TestRunRejectsInvalidPortBeforeOpeningDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.db")
	err := Run(context.Background(), path, Options{Port: 65536, NoOpen: true})
	require.ErrorContains(t, err, "port must be between 0 and 65535")
}
