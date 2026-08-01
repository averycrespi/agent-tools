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
}

func (c *idleConn) Read(p []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(p)
}

// relay copies bytes in both directions until either side closes or the idle
// window elapses, returning the byte counts.
//
// Both copies are torn down together. Leaving one half running after the other
// finishes leaks a goroutine per abandoned connection, which an agent can
// trigger at will.
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
		_ = client.SetReadDeadline(time.Now())
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(c, u)
		fromUpstream.Store(n)
		closeWrite(client)
		_ = upstream.SetReadDeadline(time.Now())
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
