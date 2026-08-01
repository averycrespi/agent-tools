//go:build e2e

package e2e_test

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// basicCredential builds the Proxy-Authorization value the proxy expects.
func basicCredential(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+token))
}

// readResponse parses an HTTP response off a raw connection.
func readResponse(conn net.Conn) (*http.Response, error) {
	return http.ReadResponse(bufio.NewReader(conn), nil)
}

// recordedRequest is what a mock upstream saw.
type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Host   string
}

// upstream is a mock server that records every request it receives.
//
// Recording is the point: several acceptance criteria are about what the
// upstream did *not* see — no injected header on a refused request, no request
// at all when injection fails atomically.
type upstream struct {
	*httptest.Server

	mu       sync.Mutex
	requests []recordedRequest
	handler  http.HandlerFunc
}

// newUpstream starts a plain-HTTP mock upstream.
func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(u)
	t.Cleanup(u.Close)
	return u
}

// newTLSUpstream starts an HTTPS mock upstream with its own certificate.
func newTLSUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewUnstartedServer(u)
	u.Server.EnableHTTP2 = true
	u.Server.StartTLS()
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.requests = append(u.requests, recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Header: r.Header.Clone(),
		Host:   r.Host,
	})
	handler := u.handler
	u.mu.Unlock()

	if handler != nil {
		handler(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("upstream ok"))
}

// SetHandler replaces the response behaviour.
func (u *upstream) SetHandler(h http.HandlerFunc) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.handler = h
}

// Requests returns everything the upstream saw.
func (u *upstream) Requests() []recordedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]recordedRequest(nil), u.requests...)
}

// Count returns how many requests reached the upstream.
func (u *upstream) Count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

// Last returns the most recent request.
func (u *upstream) Last(t *testing.T) recordedRequest {
	t.Helper()
	reqs := u.Requests()
	if len(reqs) == 0 {
		t.Fatal("the upstream received no requests")
	}
	return reqs[len(reqs)-1]
}

// HostPort returns the upstream's host and port.
func (u *upstream) HostPort(t *testing.T) (string, int) {
	t.Helper()
	host, port, err := net.SplitHostPort(u.Listener.Addr().String())
	if err != nil {
		t.Fatalf("splitting the upstream address: %v", err)
	}
	p := 0
	if _, err := fmtSscan(port, &p); err != nil {
		t.Fatalf("parsing the upstream port: %v", err)
	}
	return host, p
}

// CertPool returns a pool trusting a TLS upstream's certificate.
func (u *upstream) CertPool() *tls.Config {
	return u.Server.TLS
}

// fmtSscan is a tiny indirection so the import list stays short.
func fmtSscan(s string, v *int) (int, error) {
	n := 0
	for i := range len(s) {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errNotANumber
		}
		n = n*10 + int(c-'0')
	}
	*v = n
	return 1, nil
}

var errNotANumber = &parseError{"not a number"}

type parseError struct{ msg string }

func (e *parseError) Error() string { return e.msg }
