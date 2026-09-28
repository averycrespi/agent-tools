package httpproxy

import (
	"context"
	"errors"
	"net"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// transferError preserves the operation boundary without retaining error text.
// It exists only during forwarding; the durable projection is a closed value.
type transferError struct {
	stage string
	err   error
}

func (e *transferError) Error() string { return "HTTP transfer failed" }
func (e *transferError) Unwrap() error { return e.err }

func transferFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &transferError{stage: stage, err: err}
}

func termination(ctx context.Context, stage string, err error) *contract.HTTPTermination {
	var observed *transferError
	if errors.As(err, &observed) {
		stage = observed.stage
	}
	fact := &contract.HTTPTermination{Stage: stage, Condition: transferCondition(err)}
	// The operation error takes precedence; context state is a separate snapshot,
	// never evidence of who canceled or which condition originally caused failure.
	if ctx.Err() != nil {
		fact.Context = transferCondition(ctx.Err())
	}
	return fact
}

func transferCondition(err error) string {
	var timeout net.Error
	switch {
	case err == nil:
		return "clean"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "failure"
	}
}
