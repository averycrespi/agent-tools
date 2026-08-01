//go:build e2e

package e2e_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// interceptStack starts a stack with an intercept rule pointed at a mock
// upstream, returning both.
func interceptStack(t *testing.T, r rule, opts stackOptions) (*stack, *upstream) {
	t.Helper()

	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	r.Host = host
	r.Ports = []int{port}
	r.Mode = "intercept"

	opts.Rules = rulesDoc("deny", r)
	opts.AllowAddrs = []string{hostPort(host, port)}
	opts.UpstreamCA = up.CertPEM(t)

	return startStack(t, opts), up
}

// TestMITM is AC-2's core: an intercept rule terminates TLS, the request
// reaches the upstream, and the response comes back unmodified.
func TestMITM(t *testing.T) {
	s, up := interceptStack(t, rule{Name: "up"}, stackOptions{})
	host, port := up.HostPort(t)

	up.SetHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Upstream", "yes")
		_, _ = w.Write([]byte("hello from upstream"))
	})

	resp, body := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/thing", hostPort(host, port)))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (logs:\n%s)", resp.StatusCode, s.Logs())
	}
	if body != "hello from upstream" {
		t.Errorf("body = %q, want the upstream response unmodified", body)
	}
	if got := resp.Header.Get("X-Upstream"); got != "yes" {
		t.Errorf("X-Upstream = %q, want the upstream header passed through", got)
	}
	if up.Count() != 1 {
		t.Errorf("the upstream saw %d requests, want 1", up.Count())
	}
	if got := up.Last(t).Path; got != "/thing" {
		t.Errorf("upstream path = %q, want /thing", got)
	}
}

// TestInjection is AC-2 / V-13: the upstream sees both injected headers and
// the client sent neither.
func TestInjection(t *testing.T) {
	r := rule{
		Name: "up",
		Inject: inject(map[string]string{
			"Authorization":        "Bearer ${cred.gh_bot}",
			"X-GitHub-Api-Version": "2022-11-28",
		}, "X-Agent-Hint"),
	}
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)
	r.Host, r.Ports, r.Mode = host, []int{port}, "intercept"

	s := startStack(t, stackOptions{
		Rules:      rulesDoc("deny", r),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
		EnvCredentials: map[string]any{
			"gh_bot": map[string]any{"var": "GH_TOKEN", "hosts": []string{host}},
		},
		Env: map[string]string{"GH_TOKEN": "SENTINEL-TOKEN-VALUE"},
	})

	// The client sends its own headers, including one the rule removes.
	client := s.client()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://%s/x", hostPort(host, port)), nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("X-Agent-Hint", "should-be-removed")
	req.Header.Set("X-Client-Own", "kept")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v (logs:\n%s)", err, s.Logs())
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	seen := up.Last(t)
	if got := seen.Header.Get("Authorization"); got != "Bearer SENTINEL-TOKEN-VALUE" {
		t.Errorf("upstream Authorization = %q, want the injected credential", got)
	}
	if got := seen.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
		t.Errorf("upstream X-GitHub-Api-Version = %q, want the second injected header", got)
	}
	if got := seen.Header.Get("X-Agent-Hint"); got != "" {
		t.Errorf("X-Agent-Hint = %q, want it removed by the rule", got)
	}
	if got := seen.Header.Get("X-Client-Own"); got != "kept" {
		t.Errorf("X-Client-Own = %q, want the client's own headers preserved", got)
	}
	// Proxy-Authorization is hop-by-hop and must never reach the upstream.
	if got := seen.Header.Get("Proxy-Authorization"); got != "" {
		t.Errorf("Proxy-Authorization reached the upstream: %q", got)
	}

	// The client never held the credential: it is not in the request the
	// client built, only in what the proxy sent.
	if req.Header.Get("Authorization") != "" {
		t.Error("the client's own request must not carry the injected credential")
	}
}

// TestUnresolvableCredential is AC-8: a rule naming an absent credential
// returns 403 and the upstream sees nothing.
func TestUnresolvableCredential(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.absent}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/x", hostPort(host, port)))

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Egress-Broker-Reason"); got != "credential-unresolved" {
		t.Errorf("reason = %q, want credential-unresolved", got)
	}
	if up.Count() != 0 {
		t.Errorf("the upstream saw %d requests, want 0: nothing may reach the wire", up.Count())
	}
}

// TestCredentialHostScope is AC-9: a credential bound elsewhere is refused,
// and no bytes reach the upstream.
func TestCredentialHostScope(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.elsewhere}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
		EnvCredentials: map[string]any{
			// Bound to a host this rule will never match.
			"elsewhere": map[string]any{"var": "OTHER_TOKEN", "hosts": []string{"api.github.com"}},
		},
		Env: map[string]string{"OTHER_TOKEN": "SENTINEL-SCOPED"},
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/x", hostPort(host, port)))

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Egress-Broker-Reason"); got != "credential-host-scope" {
		t.Errorf("reason = %q, want credential-host-scope", got)
	}
	if up.Count() != 0 {
		t.Errorf("the upstream saw %d requests, want 0: no bytes on the wire", up.Count())
	}
	if strings.Contains(s.Logs(), "SENTINEL-SCOPED") {
		t.Error("the credential value appeared in the logs")
	}
}

// TestInjectionAtomicity is D17 / AC-9: a two-credential rule whose second
// credential fails leaves the upstream having seen no request at all.
func TestInjectionAtomicity(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{
				"Authorization": "Bearer ${cred.good}",
				"X-Second":      "${cred.bad}",
			}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
		EnvCredentials: map[string]any{
			"good": map[string]any{"var": "GOOD_TOKEN", "hosts": []string{host}},
			"bad":  map[string]any{"var": "BAD_TOKEN", "hosts": []string{"api.github.com"}},
		},
		Env: map[string]string{"GOOD_TOKEN": "SENTINEL-GOOD", "BAD_TOKEN": "SENTINEL-BAD"},
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/x", hostPort(host, port)))

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	// The whole point: the first credential resolved fine, but because the
	// second failed, nothing was dispatched.
	if up.Count() != 0 {
		t.Fatalf("the upstream saw %d requests, want 0: a partially injected request must never be dispatched", up.Count())
	}
	for _, sentinel := range []string{"SENTINEL-GOOD", "SENTINEL-BAD"} {
		if strings.Contains(s.Logs(), sentinel) {
			t.Errorf("credential value %q appeared in the logs", sentinel)
		}
	}
}

// TestDenyBeatsInterceptE2E is D18 through the real binary.
func TestDenyBeatsInterceptE2E(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny",
			rule{
				Name: "inject-all", Host: host, Ports: []int{port}, Path: "/**", Mode: "intercept",
				Inject: inject(map[string]string{"Authorization": "Bearer ${cred.tok}"}),
			},
			rule{Name: "no-admin", Host: host, Ports: []int{port}, Path: "/admin/**", Mode: "deny"},
		),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
		EnvCredentials: map[string]any{
			"tok": map[string]any{"var": "TOK", "hosts": []string{host}},
		},
		Env: map[string]string{"TOK": "SENTINEL-DENYWINS"},
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/admin/users", hostPort(host, port)))

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: deny must win over intercept", resp.StatusCode)
	}
	if up.Count() != 0 {
		t.Errorf("the upstream saw %d requests, want 0", up.Count())
	}

	// A path the deny rule does not cover is still injected.
	resp2, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/public", hostPort(host, port)))
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a non-denied path", resp2.StatusCode)
	}
	if got := up.Last(t).Header.Get("Authorization"); got != "Bearer SENTINEL-DENYWINS" {
		t.Errorf("Authorization = %q, want the credential injected on the allowed path", got)
	}
}

// TestImplicitAllow is AC-5's step-4 case: a host reached only via a
// path-scoped deny forwards non-matching paths with no injection.
func TestImplicitAllow(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny",
			rule{Name: "no-admin", Host: host, Ports: []int{port}, Path: "/admin/**", Mode: "deny"},
		),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/public", hostPort(host, port)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (logs:\n%s)", resp.StatusCode, s.Logs())
	}
	if got := up.Last(t).Header.Get("Authorization"); got != "" {
		t.Errorf("an implicit-allow request carried an Authorization header: %q", got)
	}

	denied, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/admin/users", hostPort(host, port)))
	if denied.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for the denied path", denied.StatusCode)
	}
}

// TestStreaming is AC-13 / V-14: a flushing upstream reaches the client
// incrementally rather than buffered to completion.
func TestStreaming(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	const chunks = 5
	up.SetHandler(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("the mock upstream cannot flush")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := range chunks {
			_, _ = fmt.Fprintf(w, "chunk %d\n", i)
			flusher.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	})

	s := startStack(t, stackOptions{
		Rules:      rulesDoc("deny", rule{Name: "up", Host: host, Ports: []int{port}, Mode: "intercept"}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
	})

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://%s/stream", hostPort(host, port)), nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		t.Fatalf("request: %v (logs:\n%s)", err, s.Logs())
	}
	defer func() { _ = resp.Body.Close() }()

	// Read in pieces and count how many separate reads returned data. A
	// buffered proxy would deliver everything in one.
	reads := 0
	buf := make([]byte, 256)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			reads++
		}
		if err != nil {
			break
		}
	}

	if reads < 2 {
		t.Errorf("the body arrived in %d read(s), want several: the response was buffered rather than streamed", reads)
	}
}

// TestPlainHTTP is AC-14: an absolute-form request goes through the same
// pipeline, with the same modes and injection.
func TestPlainHTTP(t *testing.T) {
	up := newUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.tok}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		EnvCredentials: map[string]any{
			"tok": map[string]any{"var": "TOK", "hosts": []string{host}},
		},
		Env: map[string]string{"TOK": "SENTINEL-PLAIN"},
	})

	resp, body := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("http://%s/plain", hostPort(host, port)))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (logs:\n%s)", resp.StatusCode, s.Logs())
	}
	if body != "upstream ok" {
		t.Errorf("body = %q, want the upstream response", body)
	}
	if got := up.Last(t).Header.Get("Authorization"); got != "Bearer SENTINEL-PLAIN" {
		t.Errorf("Authorization = %q, want plain HTTP injected identically to HTTPS", got)
	}
}

// TestPlainHTTPFallthroughTunnel is the shipped default: `fallthrough:
// "tunnel"` and no rules.
//
// The port-443 tunnel limit used to be applied here too, so on a fresh install
// every plain HTTP request to an unmatched host was refused with a message
// about tunnelling — undocumented, and not what AC-16 restricts.
func TestPlainHTTPFallthroughTunnel(t *testing.T) {
	up := newUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules:      rulesDoc("tunnel"),
		AllowAddrs: []string{hostPort(host, port)},
	})

	resp, body := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("http://%s/plain", hostPort(host, port)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 on the default install (logs:\n%s)", resp.StatusCode, s.Logs())
	}
	if body != "upstream ok" {
		t.Errorf("body = %q, want the upstream response", body)
	}
	if got := up.Last(t).Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want nothing injected on a fallthrough forward", got)
	}
}

// TestEncodedPathReachesUpstreamUnchanged pins that interception does not
// rewrite the request target.
//
// Rebuilding the upstream URL from the decoded path re-escapes it, so an
// encoded separator inside a segment becomes a real separator and the agent
// silently addresses a different resource.
func TestEncodedPathReachesUpstreamUnchanged(t *testing.T) {
	s, up := interceptStack(t, rule{Name: "up"}, stackOptions{})
	host, port := up.HostPort(t)

	const encoded = "/repos/o/r/contents/a%2Fb"
	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s%s", hostPort(host, port), encoded))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (logs:\n%s)", resp.StatusCode, s.Logs())
	}

	if got := up.Last(t).RawPath; got != encoded {
		t.Errorf("upstream path = %q, want %q: the encoded separator must survive interception", got, encoded)
	}
}

// TestAbsoluteFormHTTPSIsRefused guards the downgrade the absolute-form path
// used to allow.
//
// An "https://" request line is not a CONNECT: nothing about it is encrypted.
// Treating it as plain HTTP dialled the upstream in the clear and, because an
// intercept rule matches any port by default, attached the credential to a
// cleartext request — a leak an agent could trigger by choosing the request
// form.
func TestAbsoluteFormHTTPSIsRefused(t *testing.T) {
	up := newUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.tok}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		EnvCredentials: map[string]any{
			"tok": map[string]any{"var": "TOK", "hosts": []string{host}},
		},
		Env: map[string]string{"TOK": "SENTINEL-DOWNGRADE"},
	})

	conn := dialProxy(t, s)
	target := hostPort(host, port)
	writeRaw(t, conn, fmt.Sprintf("GET https://%s/secret HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		target, target, basicCredential(s.token)))

	resp, err := readResponse(conn)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (logs:\n%s)", resp.StatusCode, s.Logs())
	}
	if up.Count() != 0 {
		t.Fatalf("the upstream saw %d cleartext request(s), want 0", up.Count())
	}
}

// TestPlainHTTPRespectsDeny proves the plain path is policed, not just
// injected on.
func TestPlainHTTPRespectsDeny(t *testing.T) {
	up := newUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules:      rulesDoc("deny"),
		AllowAddrs: []string{hostPort(host, port)},
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("http://%s/plain", hostPort(host, port)))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 under fallthrough deny", resp.StatusCode)
	}
	if up.Count() != 0 {
		t.Errorf("the upstream saw %d requests, want 0", up.Count())
	}
}

// TestMITMIPLiteral is D11 / V-12: intercepting a target named by IP literal
// must complete rather than failing verification.
//
// A leaf carrying "127.0.0.1" in DNSNames instead of IPAddresses does not
// verify, and the handshake failure an agent sees is indistinguishable from a
// network fault — which is the opaque failure D11 exists to prevent.
func TestMITMIPLiteral(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	// The mock upstream listens on 127.0.0.1, so the CONNECT target is an IP
	// literal and the proxy must issue a leaf bound to it as an IP SAN.
	if host != "127.0.0.1" {
		t.Skipf("expected the mock upstream on 127.0.0.1, got %s", host)
	}

	s := startStack(t, stackOptions{
		Rules:      rulesDoc("deny", rule{Name: "ip", Host: host, Ports: []int{port}, Mode: "intercept"}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
	})

	up.SetHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("reached via ip literal"))
	})

	resp, body := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/ip", hostPort(host, port)))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: MITM of an allowed IP literal should complete (logs:\n%s)",
			resp.StatusCode, s.Logs())
	}
	if body != "reached via ip literal" {
		t.Errorf("body = %q, want the upstream response", body)
	}

	// The client verified the proxy's leaf against the proxy CA with an IP
	// server name, which only succeeds if the leaf carries an IP SAN.
	if up.Count() != 1 {
		t.Errorf("the upstream saw %d requests, want 1", up.Count())
	}
}

// TestRoundTripFailureDoesNotLeakCredential covers the one audit path that
// runs after a credential has already been placed on the outgoing request.
func TestRoundTripFailureDoesNotLeakCredential(t *testing.T) {
	const sentinel = "SENTINEL-ROUNDTRIP-FAIL"

	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.tok}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		// No UpstreamCA: injection succeeds, then the round trip fails because
		// the proxy will not trust the upstream's certificate (D12).
		EnvCredentials: map[string]any{
			"tok": map[string]any{"var": "TOK", "hosts": []string{host}},
		},
		Env: map[string]string{"TOK": sentinel},
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/x", hostPort(host, port)))
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: the upstream certificate should not verify", resp.StatusCode)
	}

	rows := waitForRows(t, s, 1)
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, sentinel) {
		t.Errorf("the credential appeared in an audit row after a round-trip failure:\n%s", joined)
	}
	if strings.Contains(s.Logs(), sentinel) {
		t.Error("the credential appeared in the process logs after a round-trip failure")
	}
}
