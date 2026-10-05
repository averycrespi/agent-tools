package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestProxyBadFirstAddressRealSocket(t *testing.T) {
	requests := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests <- string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).AddrPort().Port()
	// Keep the hostname pinned while both TCP candidates use numeric addresses.
	authority := net.JoinHostPort("approved.example", strconv.Itoa(int(port)))
	target, err := httppolicy.ParseRequest("http://"+authority+"/", "POST", authority, "", nil)
	require.NoError(t, err)
	resolver := &proxyResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("127.0.0.1")}}
	var attempted []string
	dialer := &net.Dialer{}
	factory := New(Options{Resolver: resolver, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		attempted = append(attempted, address)
		return dialer.DialContext(ctx, network, address)
	}})
	pin, err := factory.ResolveProxy(t.Context(), target.Destination(), func() []netip.AddrPort { return nil })
	require.NoError(t, err)
	response, err := pin.ProxyExchange(t.Context(), target, http.Header{}, http.NoBody, 0, true, nil)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, []string{netip.AddrPortFrom(resolver.answers[0], port).String(), netip.AddrPortFrom(resolver.answers[1], port).String()}, attempted)
	require.Equal(t, 1, resolver.calls)
	require.Len(t, requests, 1)
}

func TestProxySlowFirstSharesBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	var deadlines []time.Time
	calls := 0
	factory := New(Options{Resolver: &proxyResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("::1")}}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		deadlines = append(deadlines, deadline)
		calls++
		if calls == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}})
	destination, err := httppolicy.NewDestination("approved.example", 443)
	require.NoError(t, err)
	pin, err := factory.ResolveProxy(ctx, destination, func() []netip.AddrPort { return nil })
	require.NoError(t, err)
	conn, err := pin.Dial(ctx, true)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.Len(t, deadlines, 2)
	total, _ := ctx.Deadline()
	require.True(t, deadlines[0].Before(deadlines[1]))
	require.False(t, deadlines[1].After(total))
}

func TestProxyCancelledSuccessfulDialClosesConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	require.NoError(t, b.SetReadDeadline(time.Now().Add(time.Second)))
	factory := New(Options{DialContext: func(context.Context, string, string) (net.Conn, error) { cancel(); return a, nil }})
	destination, err := httppolicy.NewDestination("127.0.0.1", 443)
	require.NoError(t, err)
	pin, err := factory.ResolveProxy(ctx, destination, func() []netip.AddrPort { return nil })
	require.NoError(t, err)
	conn, err := pin.Dial(ctx, true)
	if conn != nil {
		_ = conn.Close()
	}
	require.ErrorIs(t, err, context.Canceled)
	_, err = b.Read(make([]byte, 1))
	require.True(t, errors.Is(err, io.EOF))
}

func TestProxyCancelledSocketClosesBeforeHandoff(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			done <- err
			return
		}
		_, err = conn.Read(make([]byte, 1))
		done <- err
	}()
	dialer := &net.Dialer{}
	factory := New(Options{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		cancel()
		return conn, err
	}})
	target, err := httppolicy.ParseRequest("http://"+listener.Addr().String()+"/", "POST", listener.Addr().String(), "", nil)
	require.NoError(t, err)
	pin, err := factory.ResolveProxy(ctx, target.Destination(), func() []netip.AddrPort { return nil })
	require.NoError(t, err)
	_, err = pin.ProxyExchange(ctx, target, http.Header{"Authorization": {"undisclosed-canary"}}, http.NoBody, 0, true, nil)
	require.ErrorIs(t, err, context.Canceled)
	select {
	case err := <-done:
		require.ErrorIs(t, err, io.EOF, "cancellation-losing socket must close without any application bytes")
	case <-time.After(2 * time.Second):
		t.Fatal("socket owner did not join")
	}
}
