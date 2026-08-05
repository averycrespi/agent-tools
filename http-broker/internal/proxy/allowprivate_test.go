package proxy

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/http-broker/internal/netguard"
	"github.com/averycrespi/agent-tools/http-broker/internal/rules"
)

// privateResolver answers every name with one RFC 1918 address, so a dial only
// happens if the guard was told to permit private space.
type privateResolver struct{}

func (privateResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("10.0.0.7")}, nil
}

// recordingSink collects audit events for assertions.
type recordingSink struct{ events []Event }

func (s *recordingSink) Record(e Event) { s.events = append(s.events, e) }

// TestAllowPrivateReachesTheDial drives a real request through the plain-HTTP
// interception path, which shares handleIntercepted with the CONNECT path.
//
// The resolver answers with a private address and the dial function ignores
// that address and connects to a loopback test server instead. So the private
// address has to survive netguard's checks for the request to succeed at all —
// which is exactly the behavior under test, with no test-only production code
// and nothing asserted about a mock.
func TestAllowPrivateReachesTheDial(t *testing.T) {
	for _, tc := range []struct {
		name         string
		allowPrivate bool
		wantStatus   int
		wantOutcome  string
	}{
		{"granted by the rule", true, http.StatusOK, OutcomeAllowed},
		{"not granted", false, http.StatusForbidden, OutcomeBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			upstreamAddr := upstream.Listener.Addr().String()
			dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, upstreamAddr)
			}

			sink := &recordingSink{}
			p := newAllowPrivateProxy(t, sink, netguard.NewWith(dial, privateResolver{}), tc.allowPrivate)

			req := httptest.NewRequest(http.MethodGet, "http://internal.test/thing", nil)
			req.Host = "internal.test"
			req.URL = &url.URL{Scheme: "http", Host: "internal.test", Path: "/thing"}
			req.RequestURI = "http://internal.test/thing"
			req.Header.Set("Proxy-Authorization", "Basic "+
				base64.StdEncoding.EncodeToString([]byte("broker:test-token")))

			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (reason %q, body %q)",
					rec.Code, tc.wantStatus, rec.Header().Get(ReasonHeader), rec.Body.String())
			}
			if len(sink.events) == 0 {
				t.Fatal("no audit event recorded")
			}
			if got := sink.events[len(sink.events)-1].Outcome; got != tc.wantOutcome {
				t.Errorf("audit outcome = %q, want %q", got, tc.wantOutcome)
			}
		})
	}
}

// TestAllowPrivateTunnelDialCarriesTheGrant covers the second dial path. The
// tunnel relay and the MITM transport are guarded separately, so a grant
// threaded through only one of them would leave the other refusing.
//
// The dial happens before the connection is hijacked, so a plain recorder is
// enough: the assertion is whether the dial was permitted, and the hijack
// failing afterwards is irrelevant to that.
func TestAllowPrivateTunnelDialCarriesTheGrant(t *testing.T) {
	for _, tc := range []struct {
		name         string
		allowPrivate bool
		wantDialed   bool
	}{
		{"granted by the rule", true, true},
		{"not granted", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialed := make(chan struct{}, 1)
			dial := func(_ context.Context, _, _ string) (net.Conn, error) {
				dialed <- struct{}{}
				client, server := net.Pipe()
				_ = server.Close()
				return client, nil
			}

			sink := &recordingSink{}
			p := newTunnelProxy(t, sink, netguard.NewWith(dial, privateResolver{}), tc.allowPrivate)

			req := httptest.NewRequest(http.MethodConnect, "//internal.test:443", nil)
			req.Host = "internal.test:443"
			req.Header.Set("Proxy-Authorization", "Basic "+
				base64.StdEncoding.EncodeToString([]byte("broker:test-token")))

			p.ServeHTTP(httptest.NewRecorder(), req)

			if gotDialed := len(dialed) == 1; gotDialed != tc.wantDialed {
				t.Errorf("dialled = %v, want %v", gotDialed, tc.wantDialed)
			}
			// A permitted tunnel records its event only after the relay ends,
			// and this recorder cannot be hijacked, so only the refusal has an
			// audit row to check here.
			if !tc.wantDialed {
				if len(sink.events) == 0 {
					t.Fatal("a refused dial must still be audited")
				}
				if got := sink.events[len(sink.events)-1].Outcome; got != OutcomeBlocked {
					t.Errorf("audit outcome = %q, want %q when the dial is refused", got, OutcomeBlocked)
				}
			}
		})
	}
}

// newAllowPrivateProxy builds a Proxy with a rule for internal.test whose
// allow_private is set as given.
func newAllowPrivateProxy(t *testing.T, sink AuditSink, dialer *netguard.Dialer, allowPrivate bool) *Proxy {
	t.Helper()

	store, err := rules.NewStore(rules.Document{
		Fallthrough: rules.FallthroughDeny,
		Rules: []rules.Rule{{
			Name: "internal", Host: "internal.test", Mode: rules.ModeIntercept,
			AllowPrivate: allowPrivate,
		}},
	})
	if err != nil {
		t.Fatalf("building the rules store: %v", err)
	}

	return New(Options{
		Rules:         store,
		Dialer:        dialer,
		Audit:         sink,
		Token:         "test-token",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		HeaderTimeout: 5 * time.Second,
		TunnelIdle:    5 * time.Second,
	})
}

// newTunnelProxy is newAllowPrivateProxy with a host-only tunnel rule, so the
// CONNECT takes the tunnel path rather than being intercepted.
func newTunnelProxy(t *testing.T, sink AuditSink, dialer *netguard.Dialer, allowPrivate bool) *Proxy {
	t.Helper()

	store, err := rules.NewStore(rules.Document{
		Fallthrough: rules.FallthroughDeny,
		Rules: []rules.Rule{{
			Name: "internal", Host: "internal.test", Mode: rules.ModeTunnel,
			AllowPrivate: allowPrivate,
		}},
	})
	if err != nil {
		t.Fatalf("building the rules store: %v", err)
	}

	return New(Options{
		Rules:         store,
		Dialer:        dialer,
		Audit:         sink,
		Token:         "test-token",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		HeaderTimeout: 5 * time.Second,
		TunnelIdle:    5 * time.Second,
	})
}
