package broker

import (
	"context"
	"time"
)

// MultiApprover fans a Review call to all approvers concurrently.
// The first non-error response wins; all others are cancelled via context.
// A timeout is applied at this level — if it fires, the call is denied
// with reason "timeout".
type MultiApprover struct {
	approvers []Approver
	timeout   time.Duration
}

// NewMultiApprover creates a MultiApprover with the given timeout and approvers.
func NewMultiApprover(timeout time.Duration, approvers ...Approver) *MultiApprover {
	return &MultiApprover{approvers: approvers, timeout: timeout}
}

func (m *MultiApprover) Review(ctx context.Context, tool string, args map[string]any) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	type result struct {
		approved     bool
		denialReason string
		err          error
	}

	ch := make(chan result, len(m.approvers))

	for _, a := range m.approvers {
		a := a
		go func() {
			approved, reason, err := a.Review(ctx, tool, args)
			ch <- result{approved, reason, err}
		}()
	}

	// Collect results; skip errors (cancelled approvers) and return first clean result.
	for range len(m.approvers) {
		select {
		case r := <-ch:
			if r.err == nil {
				cancel()
				return r.approved, r.denialReason, nil
			}
			// approver was cancelled or errored — try the next one
		case <-ctx.Done():
			return false, "timeout", nil
		}
	}
	return false, "timeout", nil
}
