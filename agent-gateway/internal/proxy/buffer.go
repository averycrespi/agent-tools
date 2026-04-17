package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

// bufferBody reads up to cap bytes from r for body-matching purposes, then
// returns a rewound reader that the caller must use to stream the body to the
// upstream (so the upstream receives all original bytes).
//
// Parameters:
//
//	ctx     – parent context; cancellation is propagated.
//	r       – the original request body.
//	hdr     – the request headers (used to check Content-Length).
//	cap     – maximum bytes to buffer for matching.
//	timeout – if > 0, caps the total time spent reading the body peek.
//
// Returns:
//
//	body      – the buffered prefix (nil when the body is empty/absent).
//	truncated – true when the body is longer than cap (body holds the first cap bytes).
//	timedOut  – true when the read deadline fired before cap bytes were read.
//	rewound   – an io.ReadCloser that yields all original body bytes:
//	              • not truncated/timedOut: bytes.NewReader(body) (original fully drained)
//	              • truncated/timedOut: io.MultiReader(buffered prefix, remaining r)
//	err       – only set on hard I/O errors (not timeout/truncation).
func bufferBody(
	ctx context.Context,
	r io.ReadCloser,
	hdr http.Header,
	cap int64,
	timeout time.Duration,
) (body []byte, truncated bool, timedOut bool, rewound io.ReadCloser, err error) {
	// Short-circuit: no body to read.
	if contentLengthIsZero(hdr) {
		return nil, false, false, io.NopCloser(bytes.NewReader(nil)), nil
	}

	// Derive a read context.
	readCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		readCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// We read up to cap+1 bytes. Reading one extra byte lets us detect whether
	// the body exceeds cap without having to read the whole thing.
	probe := make([]byte, cap+1)
	n, readErr := readWithContext(readCtx, r, probe)
	buf := probe[:n]

	switch {
	case readErr == nil && int64(n) > cap:
		// Body is longer than cap: truncated.
		truncated = true
		body = buf[:cap]
		// rewound = buffered cap bytes + the extra byte we read + remainder of r.
		rewound = io.NopCloser(io.MultiReader(
			bytes.NewReader(buf), // includes the cap+1-th byte
			r,
		))

	case isTimeout(readCtx, readErr):
		// Read deadline fired: we have a partial buffer.
		timedOut = true
		body = buf
		// rewound = what we already read + whatever remains in r.
		rewound = io.NopCloser(io.MultiReader(
			bytes.NewReader(buf),
			r,
		))

	case readErr == nil || readErr == io.EOF || readErr == io.ErrUnexpectedEOF:
		// Body fits within cap (or exactly cap bytes).
		body = buf
		// Original reader is drained; rewound replays from the buffer.
		rewound = io.NopCloser(bytes.NewReader(buf))

	default:
		// Hard read error.
		err = readErr
		rewound = io.NopCloser(io.MultiReader(bytes.NewReader(buf), r))
	}

	return body, truncated, timedOut, rewound, err
}

// contentLengthIsZero reports whether the Content-Length header is explicitly
// set to 0. A missing header returns false (the body may still be present for
// chunked transfers, etc.).
func contentLengthIsZero(hdr http.Header) bool {
	cl := hdr.Get("Content-Length")
	return cl == "0"
}

// readWithContext reads bytes from r one chunk at a time, stopping as soon as
// readCtx is done or p is full.  Unlike a bare ReadFull-in-goroutine approach,
// it never holds a concurrent reference to r when it returns, so the caller
// can safely continue reading from r after readWithContext returns.
//
// Implementation: we read in small chunks in a goroutine.  Each chunk result
// is sent on a buffered channel.  When the context fires we drain whatever
// partial result the goroutine may have just produced, then return.
func readWithContext(ctx context.Context, r io.Reader, p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}

	total := 0
	for total < len(p) {
		ch := make(chan result, 1)
		slice := p[total:]
		go func() {
			n, err := r.Read(slice)
			ch <- result{n, err}
		}()

		select {
		case <-ctx.Done():
			// Absorb whatever the goroutine read (it has already finished its
			// single Read call since ch is buffered).
			res := <-ch
			total += res.n
			return total, ctx.Err()
		case res := <-ch:
			total += res.n
			if res.err != nil {
				return total, res.err
			}
		}
	}
	return total, nil
}

// isTimeout reports whether the read error is attributable to a context
// deadline/cancellation, distinguishing it from an ordinary EOF.
func isTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
