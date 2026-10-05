package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestProxyCandidateBoundsAndFailures(t *testing.T) {
	for _, mode := range []string{"all-fail", "timeout-then-fail", "all-stall", "too-many", "empty", "mixed-forbidden", "private-denied", "cancelled", "listener-added"} {
		t.Run(mode, func(t *testing.T) {
			answers := make([]netip.Addr, contract.HTTPAddressFacts)
			for i := range answers {
				answers[i] = netip.AddrFrom4([4]byte{127, 0, 0, byte(i + 1)})
			}
			if mode == "too-many" {
				answers = append(answers, netip.MustParseAddr("::1"))
			}
			if mode == "empty" {
				answers = nil
			}
			if mode == "mixed-forbidden" {
				answers[len(answers)-1] = netip.MustParseAddr("169.254.169.254")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			deadline, _ := ctx.Deadline()
			calls := 0
			factory := New(Options{Resolver: &proxyResolver{answers: answers}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				calls++
				end, ok := ctx.Deadline()
				require.True(t, ok)
				require.False(t, end.After(deadline))
				if mode == "all-stall" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				if mode == "timeout-then-fail" && calls == 1 {
					return nil, context.DeadlineExceeded
				}
				return nil, errors.New("private-address-secret-canary")
			}})
			destination, err := httppolicy.NewDestination("approved.example", 443)
			require.NoError(t, err)
			pin, err := factory.ResolveProxy(ctx, destination, func() []netip.AddrPort {
				if mode == "listener-added" && calls > 0 {
					return []netip.AddrPort{netip.AddrPortFrom(answers[1], 443)}
				}
				return nil
			})
			if mode == "too-many" || mode == "empty" {
				require.ErrorIs(t, err, ErrProxyConnection)
				require.Zero(t, calls)
				return
			}
			require.NoError(t, err)
			if mode == "cancelled" {
				cancel()
			}
			conn, err := pin.Dial(ctx, mode != "private-denied")
			require.Nil(t, conn)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "canary")
			switch mode {
			case "all-fail":
				require.ErrorIs(t, err, ErrProxyConnection)
				require.Equal(t, contract.HTTPAddressFacts, calls)
			case "timeout-then-fail":
				require.ErrorIs(t, err, ErrProxyTimeout)
				require.Equal(t, contract.HTTPAddressFacts, calls)
			case "all-stall":
				require.ErrorIs(t, err, ErrProxyTimeout)
				require.Positive(t, calls)
				require.LessOrEqual(t, calls, contract.HTTPAddressFacts)
			case "mixed-forbidden", "private-denied":
				require.ErrorIs(t, err, ErrAddressPolicy)
				require.Zero(t, calls)
			case "cancelled":
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, calls)
			case "listener-added":
				require.ErrorIs(t, err, ErrAddressPolicy)
				require.Equal(t, 1, calls)
			}
			_, err = pin.Dial(t.Context(), true)
			require.ErrorIs(t, err, ErrAddressPolicy)
		})
	}
}

func TestProxyLateCandidateClosesWithoutBytes(t *testing.T) {
	for _, mode := range []string{"cancel", "expired", "connection-with-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a, b := net.Pipe()
			defer func() { _ = a.Close(); _ = b.Close() }()
			require.NoError(t, b.SetReadDeadline(time.Now().Add(time.Second)))
			calls := 0
			factory := New(Options{Resolver: &proxyResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}}, DialContext: func(attempt context.Context, _, _ string) (net.Conn, error) {
				calls++
				if calls > 1 {
					return nil, errors.New("refused")
				}
				switch mode {
				case "cancel":
					cancel()
				case "expired":
					<-attempt.Done()
				case "connection-with-error":
					return a, errors.New("uncertain-dial-canary")
				}
				return a, nil
			}})
			destination, err := httppolicy.NewDestination("approved.example", 443)
			require.NoError(t, err)
			pin, err := factory.ResolveProxy(ctx, destination, func() []netip.AddrPort { return nil })
			require.NoError(t, err)
			if mode == "expired" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			}
			conn, err := pin.Dial(ctx, true)
			require.Nil(t, conn)
			require.Error(t, err)
			n, readErr := b.Read(make([]byte, 1))
			require.Zero(t, n)
			require.ErrorIs(t, readErr, io.EOF)
			if mode == "cancel" {
				require.Equal(t, 1, calls)
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.Equal(t, 2, calls)
			}
		})
	}
}
