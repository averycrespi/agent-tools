//go:build e2e

package e2e_test

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMITMEndToEnd_H1(t *testing.T) {
	const want = "hello from upstream"
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, want)
	}))

	// Use the upstream URL (https://127.0.0.1:<port>) via the proxy.
	resp, err := stack.AgentClient.Get(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := string(body); got != want {
		t.Fatalf("body: got %q, want %q", got, want)
	}
}

func TestMITMEndToEnd_H2(t *testing.T) {
	const want = "hello h2"
	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, want)
	}))

	// Read the CA from the same pool used by AgentClient, but build an
	// HTTP/2-specific transport on top.
	baseTransport, ok := stack.AgentClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected transport type")
	}
	// Clone the base transport and force H2. Re-use the existing proxy function
	// (which carries the Proxy-Authorization credentials) rather than
	// constructing a new unauthenticated proxy URL.
	h2Transport := baseTransport.Clone()
	h2Transport.ForceAttemptHTTP2 = true
	h2Transport.TLSClientConfig.NextProtos = []string{"h2"}
	// h2Transport.Proxy is already set from the Clone (same auth-bearing proxy URL).

	client := &http.Client{Transport: h2Transport}

	resp, err := client.Get(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("GET via proxy (h2): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := string(body); got != want {
		t.Fatalf("body: got %q, want %q", got, want)
	}
	if resp.ProtoMajor != 2 {
		t.Fatalf("expected HTTP/2, got HTTP/%d.%d", resp.ProtoMajor, resp.ProtoMinor)
	}
}

func TestMITMEndToEnd_StreamingResponse(t *testing.T) {
	const (
		chunkCount    = 6
		chunkInterval = 100 * time.Millisecond
		minSpread     = 300 * time.Millisecond
		chunkBody     = "chunk\n"
	)

	stack := newTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Content-Type", "text/plain")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "flusher unavailable", http.StatusInternalServerError)
			return
		}
		for i := 0; i < chunkCount; i++ {
			if i > 0 {
				time.Sleep(chunkInterval)
			}
			fmt.Fprint(w, chunkBody)
			flusher.Flush()
		}
	}))

	resp, err := stack.AgentClient.Get(stack.UpstreamURL)
	if err != nil {
		t.Fatalf("GET streaming via proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}

	// Read chunks and record arrival timestamps.
	var arrivals []time.Time
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		arrivals = append(arrivals, time.Now())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan body: %v", err)
	}

	if len(arrivals) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(arrivals))
	}

	spread := arrivals[len(arrivals)-1].Sub(arrivals[0])
	if spread < minSpread {
		t.Fatalf("chunk spread %v < %v; response was not streamed", spread, minSpread)
	}
	t.Logf("streaming spread: %v across %d chunks", spread, len(arrivals))
}
