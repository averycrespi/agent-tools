package audit_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
)

// ---------------------------------------------------------------------------
// Fake clock
// ---------------------------------------------------------------------------

// fakeClock is an injectable clock for tests. Callers advance the clock with
// Advance; outstanding After channels fire when the clock reaches their target.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

type waiter struct {
	target time.Time
	ch     chan time.Time
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After returns a channel that fires when the clock is advanced past d from
// the current time.
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	target := c.now.Add(d)
	c.waiters = append(c.waiters, waiter{target: target, ch: ch})
	return ch
}

// Advance moves the fake clock forward by d, firing any pending After channels
// whose deadline has passed.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var remaining []waiter
	for _, w := range c.waiters {
		if !c.now.Before(w.target) {
			w.ch <- c.now
		} else {
			remaining = append(remaining, w)
		}
	}
	c.waiters = remaining
}

// ---------------------------------------------------------------------------
// TestPrune_RemovesRowsOlderThanRetention
// ---------------------------------------------------------------------------

// TestPrune_RemovesRowsOlderThanRetention verifies that Prune(ctx, before)
// removes rows with ts < before and leaves rows with ts >= before intact.
func TestPrune_RemovesRowsOlderThanRetention(t *testing.T) {
	db := openTestDB(t)
	logger := audit.NewLogger(db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)

	// Insert one row that is 48 hours old (should be pruned with 24h retention).
	old := audit.Entry{
		ID:           "01PRUNE000000000000000001",
		TS:           base.Add(-48 * time.Hour),
		Interception: "mitm",
		Host:         "old.example.com",
		Method:       strPtr("GET"),
		Path:         strPtr("/old"),
		DurationMS:   1,
		BytesIn:      0,
		BytesOut:     0,
		Outcome:      "forwarded",
	}
	// Insert one row that is 1 hour old (should survive with 24h retention).
	recent := audit.Entry{
		ID:           "01PRUNE000000000000000002",
		TS:           base.Add(-1 * time.Hour),
		Interception: "mitm",
		Host:         "recent.example.com",
		Method:       strPtr("GET"),
		Path:         strPtr("/recent"),
		DurationMS:   1,
		BytesIn:      0,
		BytesOut:     0,
		Outcome:      "forwarded",
	}
	require.NoError(t, logger.Record(ctx, old))
	require.NoError(t, logger.Record(ctx, recent))

	// Prune with cutoff = now - 24h: only the 48h-old row is strictly before.
	before := base.Add(-24 * time.Hour)
	n, err := logger.Prune(ctx, before)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly one old row should be pruned")

	remaining, err := logger.Query(ctx, audit.Filter{})
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, recent.ID, remaining[0].ID)
}

// ---------------------------------------------------------------------------
// TestPruneLoop_RunsAt0400
// ---------------------------------------------------------------------------

// TestPruneLoop_RunsAt0400 verifies:
//  1. RunPruneLoop fires immediately at boot.
//  2. It does NOT prune again before the tick time (03:59 advance).
//  3. It DOES prune again at the tick time (advance into 04:00).
func TestPruneLoop_RunsAt0400(t *testing.T) {
	db := openTestDB(t)
	logger := audit.NewLogger(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Pin the clock to 03:00 local time so the next 04:00 tick is 1 hour away.
	loc := time.Local
	now := time.Date(2024, 1, 15, 3, 0, 0, 0, loc)
	clk := newFakeClock(now)

	retention := 24 * time.Hour
	tickAt := 4 * time.Hour // 04:00

	// Track how many times Prune was called by counting DB deletions.
	// We seed a row older than retention so each prune call affects something
	// (or not). Instead we simply count calls via a wrapped logger.
	pruneCalls := &pruneCounter{Logger: logger}

	log := slog.Default()

	// Start the loop in the background.
	done := make(chan struct{})
	go func() {
		defer close(done)
		audit.RunPruneLoop(ctx, pruneCalls, log, retention, tickAt, clk)
	}()

	// Give the boot-time prune a moment to execute.
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 1, pruneCalls.count(), "expected 1 prune at boot")

	// Advance to 03:59 (59 minutes forward) — still before the 04:00 tick.
	clk.Advance(59 * time.Minute)
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 1, pruneCalls.count(), "no additional prune before 04:00")

	// Advance 1 more minute to reach 04:00 — the daily tick fires.
	clk.Advance(1 * time.Minute)
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 2, pruneCalls.count(), "second prune fires at 04:00")

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// TestParsePruneAt
// ---------------------------------------------------------------------------

func TestParsePruneAt(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"04:00", 4 * time.Hour, false},
		{"00:00", 0, false},
		{"23:59", 23*time.Hour + 59*time.Minute, false},
		{"12:30", 12*time.Hour + 30*time.Minute, false},
		{"badval", 0, true},
		{"25:00", 0, true},
		{"04:60", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := audit.ParsePruneAt(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// pruneCounter — wraps Logger to count Prune calls
// ---------------------------------------------------------------------------

type pruneCounter struct {
	audit.Logger
	mu    sync.Mutex
	calls int
}

func (c *pruneCounter) Prune(ctx context.Context, before time.Time) (int, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.Logger.Prune(ctx, before)
}

func (c *pruneCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// ---------------------------------------------------------------------------
// TestPruneLoop_CtxCancel
// ---------------------------------------------------------------------------

// TestPruneLoop_CtxCancel verifies the loop exits promptly when ctx is cancelled.
func TestPruneLoop_CtxCancel(t *testing.T) {
	db := openTestDB(t)
	logger := audit.NewLogger(db)
	ctx, cancel := context.WithCancel(context.Background())

	loc := time.Local
	now := time.Date(2024, 1, 15, 3, 0, 0, 0, loc)
	clk := newFakeClock(now)

	log := slog.Default()
	done := make(chan struct{})
	go func() {
		defer close(done)
		audit.RunPruneLoop(ctx, logger, log, 24*time.Hour, 4*time.Hour, clk)
	}()

	// Boot prune runs; then cancel ctx.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good: loop exited.
	case <-time.After(time.Second):
		t.Fatal("RunPruneLoop did not exit after ctx cancellation")
	}
}

// ---------------------------------------------------------------------------
// TestPruneLoop_AlreadyPastTickTime
// ---------------------------------------------------------------------------

// TestPruneLoop_AlreadyPastTickTime verifies that when the clock is already
// past tickAt, the loop schedules the NEXT day's tick (not a negative delay).
func TestPruneLoop_AlreadyPastTickTime(t *testing.T) {
	db := openTestDB(t)
	logger := audit.NewLogger(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	loc := time.Local
	// Clock is at 05:00, which is past tickAt=04:00.
	now := time.Date(2024, 1, 15, 5, 0, 0, 0, loc)
	clk := newFakeClock(now)

	pruneCalls := &pruneCounter{Logger: logger}
	log := slog.Default()

	done := make(chan struct{})
	go func() {
		defer close(done)
		audit.RunPruneLoop(ctx, pruneCalls, log, 24*time.Hour, 4*time.Hour, clk)
	}()

	// Boot prune fires.
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 1, pruneCalls.count(), "boot prune")

	// Advance 23 hours (to 04:00 next day).
	clk.Advance(23 * time.Hour)
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 2, pruneCalls.count(), "second prune fires at next day's 04:00")

	cancel()
	<-done
}
