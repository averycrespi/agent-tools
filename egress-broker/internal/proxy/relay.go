package proxy

import (
	"bufio"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// bufferedConn is a net.Conn that first drains bytes the HTTP server had
// already read into its buffer.
//
// A client is allowed to pipeline the start of its TLS handshake immediately
// after the CONNECT request. Those bytes sit in the server's bufio.Reader, and
// reading straight from the raw connection would lose them — producing a
// handshake that hangs rather than an error that explains itself.
type bufferedConn struct {
	net.Conn
	buffered *bufio.ReadWriter
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if n := c.buffered.Reader.Buffered(); n > 0 {
		return c.buffered.Read(p)
	}
	return c.Conn.Read(p)
}

// idleConn resets a deadline on every successful read or write, so a relay is
// torn down only after both directions have been quiet for the idle window.
//
// A fixed overall deadline would be wrong: a long-lived streaming response is
// legitimate, and cutting it at an arbitrary duration would look like a
// network fault to the agent.
type idleConn struct {
	net.Conn
	idle time.Duration
	// stopped lets the other copy direction end this one for good. A bare
	// deadline cannot: Read re-arms the deadline on every call, so a wake-up
	// that lands between two reads is erased and the copy parks for the whole
	// idle window anyway.
	stopped atomic.Bool
}

func (c *idleConn) Read(p []byte) (int, error) {
	// Arm first, then check. stop() sets the flag before expiring the
	// deadline, so with this order there is no interleaving in which a read
	// both misses the flag and re-arms over the expiry.
	_ = c.SetReadDeadline(time.Now().Add(c.idle))
	if c.stopped.Load() {
		return 0, net.ErrClosed
	}
	return c.Conn.Read(p)
}

// stop ends any read parked on this connection and prevents further ones.
func (c *idleConn) stop() {
	c.stopped.Store(true)
	_ = c.SetReadDeadline(time.Now())
}

func (c *idleConn) Write(p []byte) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(p)
}

// relay copies bytes in both directions until either side closes or the idle
// window elapses, returning the byte counts.
//
// A finished upstream ends the client copy too. Leaving it running after the
// upstream has hung up leaks a goroutine, two descriptors and a WaitGroup
// entry per abandoned connection — which also stalls the shutdown drain, and
// which an agent can trigger at will by holding its socket open.
//
// The reverse is deliberately not symmetric. A client that stops sending has
// only half-closed: it is still waiting for the response, so the upstream copy
// must keep running. Its own idle window bounds it.
func relay(client, upstream net.Conn, idle time.Duration) (sent, received int64) {
	c := &idleConn{Conn: client, idle: idle}
	u := &idleConn{Conn: upstream, idle: idle}

	var (
		wg           sync.WaitGroup
		toUpstream   atomic.Int64
		fromUpstream atomic.Int64
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(u, c)
		toUpstream.Store(n)
		// Signal end-of-stream upstream so a server waiting on the request
		// body can respond, rather than both sides waiting on each other.
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(c, u)
		fromUpstream.Store(n)
		closeWrite(client)
		// Nothing more can arrive for the client, so the other copy has
		// nothing left to do.
		c.stop()
	}()
	wg.Wait()

	return toUpstream.Load(), fromUpstream.Load()
}

// closeWrite half-closes a connection when the transport supports it.
type writeCloser interface{ CloseWrite() error }

func closeWrite(conn net.Conn) {
	if wc, ok := conn.(writeCloser); ok {
		_ = wc.CloseWrite()
		return
	}
	if bc, ok := conn.(*bufferedConn); ok {
		closeWrite(bc.Conn)
	}
}
