package remote

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestProxyFailurePreservesDNSAndTLSExplanation(t *testing.T) {
	for _, err := range []error{
		&net.DNSError{Name: "inventory.example", Err: "no such host"},
		errors.New("TLS handshake with inventory.example: certificate expired"),
	} {
		failure := proxyTransportFailure(err)
		require.Equal(t, ErrProxyConnection.Error(), failure.Error())
		detail := diagnostics.Snapshot("proxy", "exchange", "inventory.example:443", failure)
		require.Contains(t, detail.Explanation, "inventory.example")
		require.NotEqual(t, failure.Error(), detail.Explanation)
	}
}

type failedProxyDeadlineConn struct{ net.Conn }

func (*failedProxyDeadlineConn) SetReadDeadline(time.Time) error {
	return errors.New("private-read-deadline-canary")
}
func (*failedProxyDeadlineConn) SetWriteDeadline(time.Time) error {
	return errors.New("private-write-deadline-canary")
}
func TestProxyIdleDeadlinePreservesSafeOperation(t *testing.T) {
	conn := &proxyIdleConn{Conn: &failedProxyDeadlineConn{}}
	for _, operation := range []func([]byte) (int, error){conn.Read, conn.Write} {
		n, err := operation([]byte("private-payload-canary"))
		require.Zero(t, n)
		require.ErrorIs(t, err, ErrProxyDeadline)
		require.NotContains(t, err.Error(), "canary")
	}
}

func TestProxyTransportFailureKeepsPublicClassification(t *testing.T) {
	for _, test := range []struct{ err, want error }{
		{errors.New("private-error-canary"), ErrProxyConnection},
		{&net.DNSError{Err: "private-error-canary", Name: "private-host-canary", IsTimeout: true}, ErrProxyTimeout},
		{&url.Error{Op: "private-op-canary", URL: "private-url-canary", Err: context.DeadlineExceeded}, ErrProxyTimeout},
		{context.Canceled, context.Canceled},
	} {
		got := proxyTransportFailure(test.err)
		require.ErrorIs(t, got, test.want)
		require.NotContains(t, got.Error(), "canary")
	}
}
