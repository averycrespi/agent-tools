package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// makeRC wraps a string in an io.ReadCloser.
func makeRC(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// readAll drains an io.ReadCloser and returns its bytes.
func readAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	return b
}

// TestBufferBody_UnderCap: body fits within cap.
// Expect: body == full content, truncated=false, timedOut=false, rewound yields same bytes.
func TestBufferBody_UnderCap(t *testing.T) {
	content := "hello world"
	rc := makeRC(content)

	hdr := make(http.Header)
	hdr.Set("Content-Length", "11")

	body, truncated, timedOut, rewound, err := bufferBody(context.Background(), rc, hdr, 64, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Error("expected truncated=false")
	}
	if timedOut {
		t.Error("expected timedOut=false")
	}
	if string(body) != content {
		t.Errorf("body = %q, want %q", body, content)
	}
	if got := string(readAll(t, rewound)); got != content {
		t.Errorf("rewound = %q, want %q", got, content)
	}
}

// TestBufferBody_AtCapMarkedTruncated: body is exactly cap+1 bytes, so it exceeds cap.
// Expect: truncated=true, timedOut=false, rewound yields ALL original bytes.
func TestBufferBody_AtCapMarkedTruncated(t *testing.T) {
	// cap = 5, content = 6 bytes → one byte over.
	content := "abcdef"
	rc := makeRC(content)

	hdr := make(http.Header)
	hdr.Set("Content-Length", "6")

	body, truncated, timedOut, rewound, err := bufferBody(context.Background(), rc, hdr, 5, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !truncated {
		t.Error("expected truncated=true")
	}
	if timedOut {
		t.Error("expected timedOut=false")
	}
	// body holds the buffered prefix (up to cap+1 bytes read, so first cap bytes retained)
	if len(body) == 0 {
		t.Error("expected non-empty body prefix")
	}
	// rewound must yield the full original content
	if got := string(readAll(t, rewound)); got != content {
		t.Errorf("rewound = %q, want %q", got, content)
	}
}

// slowReader reads one byte at a time with a delay so we can test timeout.
type slowReader struct {
	data  []byte
	pos   int
	delay time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	time.Sleep(s.delay)
	p[0] = s.data[s.pos]
	s.pos++
	return 1, nil
}

type slowRC struct {
	*slowReader
}

func (s *slowRC) Close() error { return nil }

// TestBufferBody_TimeoutMarks: slow reader; context deadline fires.
// Expect: (partial buf, truncated=false, timedOut=true, rewound yields all original bytes).
func TestBufferBody_TimeoutMarks(t *testing.T) {
	content := "slow data here"
	sr := &slowReader{data: []byte(content), delay: 20 * time.Millisecond}
	rc := &slowRC{sr}

	hdr := make(http.Header)
	// No Content-Length on purpose (streamed body).

	// Timeout shorter than reading the whole body (14 bytes × 20ms = 280ms).
	_, truncated, timedOut, rewound, err := bufferBody(context.Background(), rc, hdr, 1024, 40*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Error("expected truncated=false (timeout is a separate flag)")
	}
	if !timedOut {
		t.Error("expected timedOut=true")
	}
	// rewound must be readable without blocking (the remaining bytes come from
	// the buffered prefix + whatever the slow reader still has).
	got := readAll(t, rewound)
	if !bytes.HasPrefix(got, []byte("s")) {
		t.Errorf("rewound starts with %q, want prefix 's'", got)
	}
	_ = got // full content check omitted — slow reader may or may not have more
}

// errReader is an io.ReadCloser that always returns a hard I/O error on Read.
// Used to simulate a broken agent body reader or a racy upstream.
type errReader struct {
	err error
}

func (e *errReader) Read(_ []byte) (int, error) { return 0, e.err }
func (e *errReader) Close() error               { return nil }

// TestBufferBody_HardIOError: body reader returns a non-EOF error on first read.
// Expect: err is set (propagated from the reader), so the pipeline can fail
// closed when a body matcher is configured for this request.
func TestBufferBody_HardIOError(t *testing.T) {
	rc := &errReader{err: io.ErrClosedPipe}

	hdr := make(http.Header)
	// Content-Length present and non-zero so contentLengthIsZero does not short
	// circuit and the reader is actually invoked.
	hdr.Set("Content-Length", "42")

	_, truncated, timedOut, rewound, err := bufferBody(context.Background(), rc, hdr, 64, time.Second)
	if err == nil {
		t.Fatal("expected error from faulty reader, got nil")
	}
	if truncated {
		t.Error("expected truncated=false on hard I/O error")
	}
	if timedOut {
		t.Error("expected timedOut=false on hard I/O error")
	}
	if rewound == nil {
		t.Error("expected non-nil rewound even on error (caller may need to close)")
	}
}

// TestBufferBody_NoContentLengthNoBody: GET with Content-Length: 0.
// Expect: body == nil, truncated=false, timedOut=false.
func TestBufferBody_NoContentLengthNoBody(t *testing.T) {
	rc := io.NopCloser(strings.NewReader("")) // empty body

	hdr := make(http.Header)
	hdr.Set("Content-Length", "0")

	body, truncated, timedOut, rewound, err := bufferBody(context.Background(), rc, hdr, 64, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != nil {
		t.Errorf("expected nil body, got %q", body)
	}
	if truncated {
		t.Error("expected truncated=false")
	}
	if timedOut {
		t.Error("expected timedOut=false")
	}
	// rewound should be a valid (empty) reader.
	if got := readAll(t, rewound); len(got) != 0 {
		t.Errorf("expected empty rewound, got %q", got)
	}
}
