package remote

import (
	"context"
	"errors"
	"net"
)

// Proxy failures retain only a closed transport category, never resolver,
// destination, TLS, or protocol error text. They do not authorize another dial.
var (
	ErrProxyConnection = errors.New("proxy connection failed")
	ErrProxyTimeout    = errors.New("proxy upstream timeout")
	ErrProxyDeadline   = errors.New("proxy deadline operation failed")
)

func proxyTransportFailure(err error) error {
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return ErrProxyTimeout
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return ErrProxyConnection
}
