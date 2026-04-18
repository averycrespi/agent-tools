package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Clock is an injectable time source used by RunPruneLoop.
// The real implementation wraps time.Now and time.After; tests provide a
// controllable fake.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// RealClock is the production Clock that delegates to the standard library.
type RealClock struct{}

// Now returns the current local time.
func (RealClock) Now() time.Time { return time.Now() }

// After wraps time.After.
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// ParsePruneAt parses a "HH:MM" time string into a Duration offset from
// midnight (local time). It is exported so serve.go can pass it to
// RunPruneLoop without duplicating parsing logic.
func ParsePruneAt(s string) (time.Duration, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, fmt.Errorf("audit: invalid prune_at %q: expected HH:MM", s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("audit: invalid prune_at %q: hour must be 0-23, minute 0-59", s)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute, nil
}

// nextTick returns the duration from now until the next occurrence of tickAt
// (a Duration from midnight in local time). If today's tick is still in the
// future it is used; otherwise tomorrow's is returned.
func nextTick(now time.Time, tickAt time.Duration) time.Duration {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	next := midnight.Add(tickAt)
	if !next.After(now) {
		// Already past today's tick; use tomorrow's.
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

// RunPruneLoop runs the retention pruner in a loop. It:
//  1. Fires Prune immediately at startup.
//  2. Waits until the next tickAt local time (e.g. 04:00), then fires again.
//  3. Repeats every 24h.
//
// The loop exits cleanly when ctx is cancelled.
//
// Parameters:
//   - logger: the audit Logger whose Prune method is called.
//   - retention: rows older than this are deleted on each run.
//   - tickAt: offset from midnight for the daily prune, e.g. 4*time.Hour.
//   - clk: injectable clock (use RealClock{} in production).
func RunPruneLoop(ctx context.Context, logger Logger, log *slog.Logger, retention time.Duration, tickAt time.Duration, clk Clock) {
	prune := func() {
		before := clk.Now().Add(-retention)
		n, err := logger.Prune(ctx, before)
		if err != nil {
			log.Error("audit prune failed", "err", err)
			return
		}
		log.Info("audit pruned", "rows", n, "before", before)
	}

	// Boot-time prune.
	prune()

	for {
		delay := nextTick(clk.Now(), tickAt)
		select {
		case <-ctx.Done():
			return
		case <-clk.After(delay):
			prune()
		}
	}
}
