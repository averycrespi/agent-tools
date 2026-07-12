package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestSessionDiagnosticsSeparatesFreshStaleAndNeverRunDetectors(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "session"}, SourceMeta{Path: "s.jsonl", Size: 1, ModTimeNS: 1}))
	finding := func(detector, evidence string) DetectorFinding {
		return DetectorFinding{Detector: detector, Classification: "structural", Severity: "warn", Summary: detector, EvidenceID: evidence, SourceLine: 2, Details: `{}`}
	}
	require.NoError(t, s.SaveDetectorSuccess(context.Background(), "session", "fresh", []DetectorFinding{finding("fresh", "f")}))
	require.NoError(t, s.SaveDetectorSuccess(context.Background(), "session", "zero", nil))
	require.NoError(t, s.SaveDetectorSuccess(context.Background(), "session", "stale", []DetectorFinding{finding("stale", "s")}))
	require.NoError(t, s.SaveDetectorFailure(context.Background(), "session", "stale", errors.New("detector broke")))
	require.NoError(t, s.SaveDetectorFailure(context.Background(), "session", "first-fail", errors.New("failed before findings")))

	diagnostics, err := s.SessionDiagnostics(context.Background(), "sess", []string{"first-fail", "fresh", "never", "stale", "zero"})
	require.NoError(t, err)
	require.Len(t, diagnostics.FreshFindings, 1)
	require.Equal(t, "fresh", diagnostics.FreshFindings[0].Detector)
	require.Len(t, diagnostics.StaleEvidence, 1)
	require.Equal(t, "stale", diagnostics.StaleEvidence[0].Detector)
	require.Equal(t, "detector broke", diagnostics.StaleEvidence[0].RunError)
	require.Equal(t, []DetectorStatus{
		{Detector: "first-fail", Status: "failed", Generation: 1, Error: "failed before findings"},
		{Detector: "fresh", Status: "success", Generation: 1},
		{Detector: "never", Status: "not_run"},
		{Detector: "stale", Status: "failed", Generation: 2, Error: "detector broke"},
		{Detector: "zero", Status: "success", Generation: 1},
	}, diagnostics.Detectors)
}
