package remote

import (
	"context"
	"errors"
	"net"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

// Public categories stay closed; bounded local causes survive classification.
// Neither representation authorizes another dial.
var (
	ErrProxyConnection = errors.New("proxy connection failed")
	ErrProxyTimeout    = errors.New("proxy upstream timeout")
	ErrProxyDeadline   = errors.New("proxy deadline operation failed")
)

func proxyTransportFailure(err error) error {
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return diagnostics.WithDetail(ErrProxyTimeout, diagnostics.Snapshot("remote", "connect", "", err))
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return diagnostics.WithDetail(ErrProxyConnection, diagnostics.Snapshot("remote", "connect", "", err))
}
