package remote

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixedResolver struct {
	addresses []netip.Addr
}

func (resolver fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), resolver.addresses...), nil
}

func TestParseEndpointEnforcesCanonicalHTTPSAndExactLoopbackException(t *testing.T) {
	for _, raw := range []string{"http://example.com/mcp", "https://user@example.com/mcp", "https://example.com/mcp?q=1", "https://example.com/mcp#x", "https://example.com"} {
		_, err := ParseEndpoint(raw, true)
		assert.ErrorIs(t, err, ErrInvalidURL, raw)
	}
	endpoint, err := ParseEndpoint("HTTPS://EXAMPLE.COM:443/mcp", false)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/mcp", endpoint.String())
	_, err = ParseEndpoint("http://127.0.0.1:8210/mcp", false)
	assert.ErrorIs(t, err, ErrInvalidURL)
	endpoint, err = ParseEndpoint("http://127.0.0.1:8210/mcp", true)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:8210/mcp", endpoint.String())
	_, err = ParseEndpoint("https://bad_host/mcp", false)
	assert.ErrorIs(t, err, ErrInvalidURL)
	trusted, err := Parse("https://private.example/mcp", Policy{AllowRestricted: true})
	require.NoError(t, err)
	assert.True(t, allowedAddress(netip.MustParseAddr("10.0.0.1"), trusted.allowRestricted))
}

func TestFactoryPinsFreshValidatedAddressAndRejectsReservedAnswers(t *testing.T) {
	endpoint, err := ParseEndpoint("https://example.com/mcp", false)
	require.NoError(t, err)
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "192.0.2.1", "2001:db8::1"} {
		factory := New(Options{Resolver: fixedResolver{addresses: []netip.Addr{netip.MustParseAddr(address)}}, DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("reserved address was dialed")
			return nil, nil
		}})
		_, dialErr := factory.dialPinned(context.Background(), "tcp", endpoint)
		assert.ErrorIs(t, dialErr, ErrAddressPolicy, address)
	}
	var target string
	factory := New(Options{Resolver: fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
		target = address
		return nil, errors.New("stop")
	}})
	_, err = factory.dialPinned(context.Background(), "tcp", endpoint)
	assert.Equal(t, "93.184.216.34:443", target)
	assert.EqualError(t, err, "stop")
}

func TestFactoryMarksImmediatelyBeforeRoundTripperHandoff(t *testing.T) {
	endpoint, err := ParseEndpoint("https://example.com/mcp", false)
	require.NoError(t, err)
	var marked atomic.Bool
	factory := New(Options{
		Resolver: fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			assert.True(t, marked.Load(), "dial began before RoundTripper handoff marker")
			return nil, errors.New("dial stopped")
		},
	})
	_, err = factory.Exchange(context.Background(), Request{Endpoint: endpoint, Method: http.MethodPost, Header: http.Header{}, MaxBody: 16, BeforeRoundTrip: func() { marked.Store(true) }})
	assert.Error(t, err)
	assert.True(t, marked.Load())
}

func TestFactoryRetainsPlatformTLSVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	endpoint, err := Parse(server.URL+"/mcp", Policy{AllowRestricted: true})
	require.NoError(t, err)
	_, err = New(Options{}).Exchange(context.Background(), Request{Endpoint: endpoint, Method: http.MethodPost, Header: http.Header{}, MaxBody: 16})
	assert.Error(t, err)
}

func TestExchangeHasNoProxyRedirectCookieOrUnboundedBodyAndCancelsBlockedRead(t *testing.T) {
	var sawCookie atomic.Bool
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Cookie") != "" {
			sawCookie.Store(true)
		}
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(writer, request, "/final", http.StatusFound)
		case "/large":
			_, _ = writer.Write([]byte(strings.Repeat("x", 17)))
		case "/blocked":
			close(started)
			writer.Header().Set("Content-Type", contract.MediaTypeJSON)
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
			close(cancelled)
		default:
			_, _ = writer.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	factory := New(Options{})
	base := server.URL
	for path, expected := range map[string]error{"/redirect": ErrRedirect, "/large": ErrResponseLimit} {
		endpoint, err := ParseEndpoint(base+path, true)
		require.NoError(t, err)
		_, err = factory.Exchange(context.Background(), Request{Endpoint: endpoint, Method: http.MethodPost, Header: http.Header{}, MaxBody: 16})
		assert.ErrorIs(t, err, expected)
	}
	endpoint, err := ParseEndpoint(base+"/blocked", true)
	require.NoError(t, err)
	_, err = factory.Exchange(context.Background(), Request{Endpoint: endpoint, Method: http.MethodPost, Header: http.Header{"X-Test": []string{"bad\nvalue"}}, MaxBody: 16})
	assert.ErrorIs(t, err, ErrResponseLimit)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, exchangeErr := factory.Exchange(ctx, Request{Endpoint: endpoint, Method: http.MethodPost, Header: http.Header{}, MaxBody: 16})
		done <- exchangeErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocked response did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked response did not terminate after cancellation")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe response cancellation")
	}
	assert.False(t, sawCookie.Load())
}
