package remote

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestProxyCandidateTLSAndPostWriteNoReplay(t *testing.T) {
	for _, mode := range []string{"verified", "wrong-host", "untrusted", "post-write-disconnect"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int64
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if string(body) != "single-use-body-canary" || r.Header.Get("Authorization") != "approved-secret-canary" {
					t.Error("lost body or selected credential")
				}
				if mode == "post-write-disconnect" {
					conn, _, hijackErr := http.NewResponseController(w).Hijack()
					if hijackErr != nil {
						t.Error(hijackErr)
						return
					}
					_ = conn.Close()
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			roots := x509.NewCertPool()
			if mode != "untrusted" {
				roots.AddCert(upstream.Certificate())
			}
			host := upstream.Certificate().DNSNames[0]
			if mode == "wrong-host" {
				host = "wrong.example"
			}
			port := upstream.Listener.Addr().(*net.TCPAddr).Port
			authority := net.JoinHostPort(host, strconv.Itoa(port))
			target, err := httppolicy.ParseRequest("https://"+authority+"/upload", "POST", authority, "", nil)
			require.NoError(t, err)
			resolver := &proxyResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.3")}}
			var dials atomic.Int64
			dialer := &net.Dialer{}
			factory := New(Options{Resolver: resolver, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				dials.Add(1)
				return dialer.DialContext(ctx, network, address)
			}})
			pin, err := factory.ResolveProxy(t.Context(), target.Destination(), func() []netip.AddrPort { return nil })
			require.NoError(t, err)
			body := &countedProxyBody{Reader: strings.NewReader("single-use-body-canary")}
			response, err := pin.ProxyExchange(t.Context(), target, http.Header{"Authorization": {"approved-secret-canary"}, "Idempotency-Key": {"not-replay-permission"}}, body, int64(body.Len()), true, roots)
			if mode == "verified" {
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
			} else {
				require.ErrorIs(t, err, ErrProxyConnection)
				require.NotContains(t, err.Error(), "canary")
			}
			require.EqualValues(t, 2, dials.Load(), "TLS or post-write failure must not try the third address")
			require.Equal(t, 1, resolver.calls)
			if mode == "verified" || mode == "post-write-disconnect" {
				require.EqualValues(t, 1, requests.Load())
			} else {
				require.Zero(t, requests.Load())
				require.Zero(t, body.reads.Load(), "TLS validation precedes body disclosure")
			}
			require.Eventually(t, body.closed.Load, time.Second, time.Millisecond)
			_, err = pin.ProxyExchange(t.Context(), target, http.Header{}, http.NoBody, 0, true, roots)
			require.ErrorIs(t, err, ErrAddressPolicy)
			require.EqualValues(t, 2, dials.Load())
		})
	}
}

type countedProxyBody struct {
	*strings.Reader
	reads  atomic.Int64
	closed atomic.Bool
}

func (b *countedProxyBody) Read(p []byte) (int, error) { b.reads.Add(1); return b.Reader.Read(p) }
func (b *countedProxyBody) Close() error               { b.closed.Store(true); return nil }

func TestProxyIPv6SocketFallback(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT) {
		t.Skip("IPv6 loopback unavailable; real IPv6 qualification requires an enabled runner")
	}
	require.NoError(t, err)
	var requests atomic.Int64
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = io.Copy(w, r.Body) }))
	require.NoError(t, upstream.Listener.Close())
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()
	authority := net.JoinHostPort("approved.example", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	target, err := httppolicy.ParseRequest("http://"+authority+"/", "POST", authority, "", nil)
	require.NoError(t, err)
	factory := New(Options{Resolver: &proxyResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("::1")}}})
	pin, err := factory.ResolveProxy(t.Context(), target.Destination(), func() []netip.AddrPort { return nil })
	require.NoError(t, err)
	payload := "ipv6-one-shot-body"
	response, err := pin.ProxyExchange(t.Context(), target, http.Header{}, io.NopCloser(strings.NewReader(payload)), int64(len(payload)), true, nil)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, payload, string(body))
	require.EqualValues(t, 1, requests.Load())
}

func TestProxyExchangeCancellationJoinsDialOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var dials atomic.Int64
	factory := New(Options{Resolver: &proxyResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		dials.Add(1)
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release // Cancellation is not proof an uncooperative native owner exited.
		return nil, ctx.Err()
	}})
	target, err := httppolicy.ParseRequest("http://approved.example/", "POST", "approved.example", "", nil)
	require.NoError(t, err)
	pin, err := factory.ResolveProxy(ctx, target.Destination(), func() []netip.AddrPort { return nil })
	require.NoError(t, err)
	body := &countedProxyBody{Reader: strings.NewReader("never-dispatched")}
	done := make(chan error, 1)
	go func() {
		_, err := pin.ProxyExchange(ctx, target, http.Header{}, body, int64(body.Len()), true, nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("dial did not observe request cancellation")
	}
	select {
	case <-done:
		t.Fatal("exchange abandoned its dial owner")
	default:
	}
	close(release)
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("dial owner did not join")
	}
	require.EqualValues(t, 1, dials.Load())
	require.True(t, body.closed.Load())
	require.Zero(t, body.reads.Load())
}
