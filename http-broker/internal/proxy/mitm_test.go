package proxy

import (
	"io"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"
)

// TestServeH1ReleasesTheConnection is a regression test.
//
// serveH1 hands one already-accepted connection to an http.Server through a
// fake listener. The first version never closed that listener, so Accept
// blocked forever after handing the connection over, Serve never returned, and
// serveH1's caller never unwound — leaking a goroutine and a TLS connection on
// every intercepted HTTP/1.1 connection, which an agent can produce at will.
func TestServeH1ReleasesTheConnection(t *testing.T) {
	p := &Proxy{headerTimeout: 5 * time.Second}

	server, client := net.Pipe()

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		p.serveH1(server, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
	}()

	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := client.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("writing the request: %v", err)
	}
	if _, err := io.ReadAll(client); err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	_ = client.Close()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("serveH1 did not return after the connection closed: the listener is never closed, so Serve blocks forever and the goroutine leaks")
	}
}

// TestRelayTearsDownBothHalves is a regression test.
//
// Each copy direction wakes the other by expiring its read deadline. The wake
// used to be applied to the socket the finishing goroutine had itself been
// reading, which unblocks nothing: when an upstream closed while the agent held
// its end open, the client→upstream copy stayed parked for the whole idle
// window, holding two descriptors and a WaitGroup entry that also stalls the
// shutdown drain.
func TestRelayTearsDownBothHalves(t *testing.T) {
	clientSide, clientPeer := tcpPair(t)
	upstreamSide, upstreamPeer := tcpPair(t)

	// The agent stays connected and silent, which is the ordinary case for an
	// idle tunnel.
	defer func() { _ = clientPeer.Close() }()

	// The upstream hangs up.
	if err := upstreamPeer.Close(); err != nil {
		t.Fatalf("closing the upstream peer: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay(clientSide, upstreamSide, time.Minute)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not return after the upstream closed: the client→upstream copy was never woken, so it waits out the full idle window")
	}
}

// TestRelaySurvivesClientHalfClose is the other side of that teardown: a
// client that has finished sending is still waiting for the response, so the
// upstream copy must keep running after the client copy ends.
func TestRelaySurvivesClientHalfClose(t *testing.T) {
	clientSide, clientPeer := tcpPair(t)
	upstreamSide, upstreamPeer := tcpPair(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		relay(clientSide, upstreamSide, time.Minute)
	}()

	if _, err := clientPeer.Write([]byte("request")); err != nil {
		t.Fatalf("writing the request: %v", err)
	}
	// Half-close: done sending, still reading.
	if err := clientPeer.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half-closing the client: %v", err)
	}

	if err := upstreamPeer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, len("request"))
	if _, err := io.ReadFull(upstreamPeer, buf); err != nil {
		t.Fatalf("the upstream never saw the request: %v", err)
	}
	if _, err := upstreamPeer.Write([]byte("response")); err != nil {
		t.Fatalf("writing the response: %v", err)
	}
	_ = upstreamPeer.Close()

	if err := clientPeer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	got, err := io.ReadAll(clientPeer)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if string(got) != "response" {
		t.Errorf("client read %q, want %q: a half-closed client must still receive its response", got, "response")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not return after the exchange finished")
	}
}

// tcpPair returns the two ends of a connected loopback TCP connection.
//
// Real sockets rather than net.Pipe: the relay half-closes with CloseWrite,
// which only a TCP connection implements.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type accepted struct {
		conn net.Conn
		err  error
	}
	accept := make(chan accepted, 1)
	go func() {
		conn, err := ln.Accept()
		accept <- accepted{conn, err}
	}()

	dialed, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	a := <-accept
	if a.err != nil {
		t.Fatalf("accepting: %v", a.err)
	}

	t.Cleanup(func() {
		_ = dialed.Close()
		_ = a.conn.Close()
	})
	return a.conn, dialed
}

// TestServeH1DoesNotLeakGoroutines exercises the same property in aggregate,
// which is what actually matters: sustained agent traffic must not grow the
// goroutine count without bound.
func TestServeH1DoesNotLeakGoroutines(t *testing.T) {
	p := &Proxy{headerTimeout: 5 * time.Second}

	settle := func() {
		for range 5 {
			runtime.GC()
			time.Sleep(20 * time.Millisecond)
		}
	}

	settle()
	before := runtime.NumGoroutine()

	for range 25 {
		server, client := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.serveH1(server, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("ok"))
			}))
		}()

		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := client.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")); err != nil {
			t.Fatalf("writing the request: %v", err)
		}
		_, _ = io.ReadAll(client)
		_ = client.Close()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("serveH1 did not return")
		}
	}

	settle()
	after := runtime.NumGoroutine()

	// A small delta is normal scheduler noise; 25 leaked connections would not
	// be small.
	if after-before > 10 {
		t.Errorf("goroutine count grew from %d to %d over 25 connections; serveH1 is leaking", before, after)
	}
}

// TestSingleConnListenerAcceptUnblocksOnClose pins the mechanism directly.
func TestSingleConnListenerAcceptUnblocksOnClose(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()

	l := newSingleConnListener(server)

	got, err := l.Accept()
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	if got != server {
		t.Error("first Accept should return the wrapped connection")
	}

	blocked := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		blocked <- err
	}()

	select {
	case <-blocked:
		t.Fatal("the second Accept returned immediately; it must block until Close")
	case <-time.After(100 * time.Millisecond):
	}

	_ = l.Close()

	select {
	case err := <-blocked:
		if err == nil {
			t.Error("Accept after Close should return an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not unblock after Close")
	}

	// Close is idempotent: ConnState can fire more than once.
	_ = l.Close()
	_ = l.Close()
}
