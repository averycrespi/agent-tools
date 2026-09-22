package remote

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

type proxyResolver struct {
	answers []netip.Addr
	calls   int
}

func (r *proxyResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	return r.answers, nil
}

func TestProxyPinnedAddressesAndDynamicExclusions(t *testing.T) {
	for _, test := range []struct {
		name    string
		answers []netip.Addr
		private bool
		allow   bool
	}{
		{"public", []netip.Addr{netip.MustParseAddr("93.184.216.34")}, false, true},
		{"private-refused", []netip.Addr{netip.MustParseAddr("127.0.0.1")}, false, false},
		{"private-explicit", []netip.Addr{netip.MustParseAddr("127.0.0.1")}, true, true},
		{"mixed-metadata", []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("169.254.169.254")}, true, false},
		{"reserved", []netip.Addr{netip.MustParseAddr("192.0.2.1")}, true, false},
		{"v6-metadata", []netip.Addr{netip.MustParseAddr("fd00:ec2::254")}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := &proxyResolver{answers: test.answers}
			dials := 0
			f := New(Options{Resolver: resolver, DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
				dials++
				require.Equal(t, netip.AddrPortFrom(test.answers[0], 443).String(), address)
				a, b := net.Pipe()
				_ = b.Close()
				return a, nil
			}})
			target, err := httppolicy.NewDestination("api.example.com", 443)
			require.NoError(t, err)
			var listeners []netip.AddrPort
			pin, err := f.ResolveProxy(t.Context(), target, func() []netip.AddrPort { return listeners })
			require.NoError(t, err)
			resolver.answers = []netip.Addr{netip.MustParseAddr("169.254.169.254")}
			conn, err := pin.Dial(t.Context(), test.private)
			if test.allow {
				require.NoError(t, err)
				_ = conn.Close()
				require.Equal(t, 1, dials)
			} else {
				require.Error(t, err)
				require.Zero(t, dials)
			}
			_, err = pin.Dial(t.Context(), true)
			require.Error(t, err)
			require.Equal(t, 1, resolver.calls)
			resolver.answers = test.answers
			pin, err = f.ResolveProxy(t.Context(), target, func() []netip.AddrPort { return listeners })
			require.NoError(t, err)
			listeners = []netip.AddrPort{netip.AddrPortFrom(test.answers[0], 443)}
			_, err = pin.Dial(t.Context(), true)
			require.Error(t, err)
		})
	}
}

func TestProxyOneShotDoesNotRetryOrFollowRedirect(t *testing.T) {
	for _, mode := range []string{"redirect", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if mode == "redirect" {
					w.Header().Set("Location", "http://127.0.0.1:1/secret")
					w.WriteHeader(307)
					return
				}
				c, _, err := http.NewResponseController(w).Hijack()
				if err == nil {
					_ = c.Close()
				}
			}))
			defer upstream.Close()
			target, err := httppolicy.ParseRequest(upstream.URL+"/", "GET", upstream.Listener.Addr().String(), "", nil)
			require.NoError(t, err)
			factory := New(Options{})
			pin, err := factory.ResolveProxy(t.Context(), target.Destination(), func() []netip.AddrPort { return nil })
			require.NoError(t, err)
			response, err := pin.ProxyExchange(t.Context(), target, http.Header{"Idempotency-Key": []string{"fixture"}}, http.NoBody, 0, true, nil)
			if mode == "redirect" {
				require.NoError(t, err)
				require.Equal(t, 307, response.StatusCode)
				_, _ = io.Copy(io.Discard, response.Body)
				require.NoError(t, response.Body.Close())
			} else {
				require.Error(t, err)
			}
			require.EqualValues(t, 1, requests.Load())
		})
	}
}
