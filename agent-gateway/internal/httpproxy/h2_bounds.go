package httpproxy

import (
	"crypto/tls"
	"net"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// h2BoundConn observes only frame boundaries in decrypted bytes, never HPACK or
// policy. x/net/http2 owns protocol validation. A phase deadline starts with the
// first frame byte, and a HEADERS/CONTINUATION block cannot renew it by trickling
// frames. Between complete frames no header deadline caps an active SSE stream.
// ServeConn ignores http.Server.ReadHeaderTimeout in the pinned x/net version.
type h2BoundConn struct {
	net.Conn
	state         tls.ConnectionState
	preface       int
	header        [9]byte
	headerBytes   int
	remaining     int
	headerBlock   bool
	endHeaders    bool
	phaseDeadline time.Time
	blockDeadline time.Time
	timeout       time.Duration
}

func newH2BoundConn(conn *tls.Conn) *h2BoundConn {
	return &h2BoundConn{Conn: conn, state: conn.ConnectionState(), preface: 24, timeout: contract.HTTPProxyHeaderTimeout}
}
func (c *h2BoundConn) ConnectionState() tls.ConnectionState { return c.state }

func (c *h2BoundConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.preface > 0 {
		n, err := c.Conn.Read(p[:min(len(p), c.preface)])
		c.preface -= n
		return n, err
	}
	if c.remaining > 0 {
		n, err := c.Conn.Read(p[:min(len(p), c.remaining)])
		c.remaining -= n
		if c.remaining == 0 {
			if finishErr := c.finishFrame(); err == nil {
				err = finishErr
			}
		}
		return n, err
	}
	n, err := c.Conn.Read(p[:min(len(p), 9-c.headerBytes)])
	if n == 0 {
		return n, err
	}
	if c.headerBytes == 0 {
		c.phaseDeadline = time.Now().Add(c.timeout)
		if c.headerBlock {
			c.phaseDeadline = c.blockDeadline
		}
		if deadlineErr := c.SetReadDeadline(c.phaseDeadline); deadlineErr != nil {
			return n, deadlineErr
		}
	}
	copy(c.header[c.headerBytes:], p[:n])
	c.headerBytes += n
	if c.headerBytes == 9 {
		c.remaining = int(c.header[0])<<16 | int(c.header[1])<<8 | int(c.header[2])
		kind := c.header[3]
		if kind == 1 && !c.headerBlock {
			c.headerBlock = true
			c.blockDeadline = c.phaseDeadline
		}
		c.endHeaders = (kind == 1 || kind == 9) && c.header[4]&4 != 0
		if c.remaining == 0 {
			if finishErr := c.finishFrame(); err == nil {
				err = finishErr
			}
		}
	}
	return n, err
}
func (c *h2BoundConn) finishFrame() error {
	c.headerBytes = 0
	if c.endHeaders {
		c.headerBlock = false
	}
	deadline := time.Time{}
	if c.headerBlock {
		deadline = c.blockDeadline
	}
	return c.SetReadDeadline(deadline)
}
