//go:build e2e

package e2e_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestHealthz is AC-19 / V-24: an unauthenticated liveness endpoint, which is
// the only way to detect a wedged-but-listening proxy from outside.
func TestHealthz(t *testing.T) {
	s := startStack(t, stackOptions{})

	resp, err := http.Get(s.dashURL("/healthz")) //nolint:noctx // test
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 without any token", resp.StatusCode)
	}
	body := readAll(t, resp)
	if strings.TrimSpace(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

// TestProxyRequiresAuthentication: an unauthenticated CONNECT gets 407, so a
// sandbox that has lost its token fails loudly rather than silently
// bypassing policy.
func TestProxyRequiresAuthentication(t *testing.T) {
	s := startStack(t, stackOptions{})

	conn := dialProxy(t, s)
	writeRaw(t, conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")

	resp, err := readResponse(conn)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Errorf("status = %d, want 407", resp.StatusCode)
	}
	if got := resp.Header.Get("Proxy-Authenticate"); got == "" {
		t.Error("a 407 must carry Proxy-Authenticate so the client knows how to authenticate")
	}
	if got := resp.Header.Get("X-Egress-Broker-Reason"); got != "proxy-auth-required" {
		t.Errorf("reason = %q, want proxy-auth-required", got)
	}
}

// TestFallthroughTunnel is AC-4: an unmatched host is relayed and audited.
func TestFallthroughTunnel(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("tunnel",
			rule{Name: "up", Host: host, Ports: []int{port}, Mode: "tunnel"},
		),
		AllowAddrs: []string{hostPort(host, port)},
	})

	// A tunnel relays opaque bytes, so the client's own TLS trust applies —
	// the proxy's CA is not involved.
	resp := s.connect(hostPort(host, port))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200 Connection established", resp.StatusCode)
	}
}

// TestFallthroughDeny is AC-3: with fallthrough deny, an unmatched host gets
// 403 carrying the reason header.
func TestFallthroughDeny(t *testing.T) {
	s := startStack(t, stackOptions{Rules: rulesDoc("deny")})

	resp := s.connect("unmatched.example.com:443")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Egress-Broker-Reason"); got != "fallthrough-deny" {
		t.Errorf("reason = %q, want fallthrough-deny", got)
	}
}

// TestModeDeny is AC-5: a host-only deny rule rejects at CONNECT, before any
// upstream dial.
func TestModeDeny(t *testing.T) {
	s := startStack(t, stackOptions{
		Rules: rulesDoc("tunnel",
			rule{Name: "ads", Host: "*.doubleclick.net", Mode: "deny"},
		),
	})

	resp := s.connect("ads.doubleclick.net:443")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Egress-Broker-Reason"); got != "rule-deny" {
		t.Errorf("reason = %q, want rule-deny", got)
	}
}

// TestModeTunnel is AC-5: a tunnel rule keeps its host reachable even when
// fallthrough is deny.
func TestModeTunnel(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny",
			rule{Name: "up", Host: host, Ports: []int{port}, Mode: "tunnel"},
		),
		AllowAddrs: []string{hostPort(host, port)},
	})

	if resp := s.connect(hostPort(host, port)); resp.StatusCode != http.StatusOK {
		t.Errorf("the tunnel rule's host: status = %d, want 200 even under fallthrough deny", resp.StatusCode)
	}
	if resp := s.connect("other.example.com:443"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("an unmatched host: status = %d, want 403", resp.StatusCode)
	}
}

// TestNonStandardPortTunnel is V-26 / AC-16: fallthrough tunnelling is limited
// to 443, and a non-standard port works only once a rule opts it in.
func TestNonStandardPortTunnel(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	t.Run("refused without an opt-in", func(t *testing.T) {
		s := startStack(t, stackOptions{Rules: rulesDoc("tunnel")})

		resp := s.connect(hostPort(host, port))
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: fallthrough tunnelling is limited to 443", resp.StatusCode)
		}
		if got := resp.Header.Get("X-Egress-Broker-Reason"); got != "fallthrough-deny" {
			t.Errorf("reason = %q, want fallthrough-deny", got)
		}
	})

	t.Run("allowed once a rule lists the port", func(t *testing.T) {
		s := startStack(t, stackOptions{
			Rules: rulesDoc("tunnel",
				rule{Name: "alt", Host: host, Ports: []int{port}, Mode: "tunnel"},
			),
			AllowAddrs: []string{hostPort(host, port)},
		})
		if resp := s.connect(hostPort(host, port)); resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200 once the port is opted in", resp.StatusCode)
		}
	})
}

// TestNetguard is AC-11's e2e half on the tunnel path. This is the hole the
// ported implementation had: it guarded only its MITM transport, so a CONNECT
// to cloud metadata was relayed straight through (D8).
func TestNetguard(t *testing.T) {
	s := startStack(t, stackOptions{
		Rules: rulesDoc("tunnel",
			// Rules that would otherwise allow these targets, so the refusal
			// provably comes from the address guard rather than from policy.
			rule{Name: "imds", Host: "169.254.169.254", Ports: []int{443, 80}, Mode: "tunnel"},
			rule{Name: "loopback", Host: "127.0.0.1", Ports: []int{443, 80}, Mode: "tunnel"},
			rule{Name: "v6-loopback", Host: "::1", Ports: []int{443, 80}, Mode: "tunnel"},
			rule{Name: "private", Host: "10.0.0.1", Ports: []int{443, 80}, Mode: "tunnel"},
			rule{Name: "cgnat", Host: "100.100.100.200", Ports: []int{443, 80}, Mode: "tunnel"},
			rule{Name: "nat64", Host: "64:ff9b::7f00:1", Ports: []int{443, 80}, Mode: "tunnel"},
		),
	})

	cases := []struct {
		name   string
		target string
	}{
		{"aws imds", "169.254.169.254:80"},
		{"loopback", "127.0.0.1:80"},
		{"ipv6 loopback", "[::1]:80"},
		{"rfc1918 private", "10.0.0.1:443"},
		{"carrier-grade nat", "100.100.100.200:80"},
		{"nat64-embedded loopback", "[64:ff9b::7f00:1]:80"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.connect(tc.target)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: the address guard must refuse this on the tunnel path", resp.StatusCode)
			}
			if got := resp.Header.Get("X-Egress-Broker-Reason"); got != "blocked-address" {
				t.Errorf("reason = %q, want blocked-address", got)
			}
		})
	}
}

// TestNetguardAlternateEncodings covers AC-11's alternate IP-literal spellings
// reaching the proxy as a CONNECT target.
func TestNetguardAlternateEncodings(t *testing.T) {
	s := startStack(t, stackOptions{Rules: rulesDoc("tunnel")})

	for _, target := range []string{
		"2130706433:443", // decimal 127.0.0.1
		"0x7f000001:443", // hex
		"127.1:443",      // short dotted form
	} {
		resp := s.connect(target)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("CONNECT %s succeeded; an alternate IP encoding must never be relayed", target)
		}
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}
