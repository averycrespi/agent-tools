//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRulesReload is V-5 / AC-7: SIGHUP swaps the ruleset live, and an invalid
// file leaves the previous rules serving while logging an error.
func TestRulesReload(t *testing.T) {
	s := startStack(t, stackOptions{
		Rules: rulesDoc("tunnel",
			rule{Name: "ads", Host: "*.doubleclick.net", Mode: "deny"},
		),
	})

	// Before: the deny rule is in force.
	if resp := s.connect("ads.doubleclick.net:443"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("before reload: status = %d, want 403", resp.StatusCode)
	}

	// Swap in a ruleset that no longer denies that host, and reload.
	s.writeRules(rulesDoc("tunnel"))
	s.reload()

	// After: the host is no longer denied. It now falls through to tunnel,
	// and the dial fails because nothing is listening — but the refusal is a
	// dial failure, not a policy deny, which is what proves the swap landed.
	resp := s.connect("ads.doubleclick.net:443")
	if resp.StatusCode == http.StatusForbidden &&
		resp.Header.Get("X-Egress-Broker-Reason") == "rule-deny" {
		t.Error("after reload the old deny rule is still in force; the swap did not take effect")
	}

	// The reload was logged.
	if !strings.Contains(s.Logs(), "reloaded") {
		t.Errorf("the reload should be logged:\n%s", s.Logs())
	}
}

// TestRulesReloadKeepsPreviousOnInvalidFile is the safety half of AC-7. A typo
// in rules.json must not take the sandbox's network down.
func TestRulesReloadKeepsPreviousOnInvalidFile(t *testing.T) {
	s := startStack(t, stackOptions{
		Rules: rulesDoc("tunnel",
			rule{Name: "ads", Host: "*.doubleclick.net", Mode: "deny"},
		),
	})

	if resp := s.connect("ads.doubleclick.net:443"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("before reload: status = %d, want 403", resp.StatusCode)
	}

	// Malformed JSON.
	s.writeRulesRaw(`{"fallthrough": "tunnel", "rules": [ {"name": }`)
	s.reload()

	resp := s.connect("ads.doubleclick.net:443")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("after an invalid reload: status = %d, want 403 — the previous ruleset must keep serving", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Egress-Broker-Reason"); got != "rule-deny" {
		t.Errorf("reason = %q, want rule-deny from the previous ruleset", got)
	}
	if !strings.Contains(s.Logs(), "rules unchanged") {
		t.Errorf("the failed reload should say the rules are unchanged:\n%s", s.Logs())
	}

	// A semantically invalid document (a public-suffix host) behaves the same.
	s.writeRules(rulesDoc("tunnel", rule{Name: "too-broad", Host: "*.com", Mode: "tunnel"}))
	s.reload()

	if resp := s.connect("ads.doubleclick.net:443"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("after an invalid ruleset: status = %d, want the previous rules still serving", resp.StatusCode)
	}
}

// TestReloadIsTheKillSwitch is D15: recovery is a rules edit plus SIGHUP, not
// a process kill. Killing the process would point every sandbox's baked-in
// proxy variables at a dead socket.
func TestReloadIsTheKillSwitch(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.tok}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
		EnvCredentials: map[string]any{
			"tok": map[string]any{"var": "TOK", "hosts": []string{host}},
		},
		Env: map[string]string{"TOK": "SENTINEL-KILLSWITCH"},
	})

	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("https://%s/x", hostPort(host, port)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 before the kill switch", resp.StatusCode)
	}

	// The kill switch: replace every intercept rule with a tunnel rule.
	// Injection stops immediately and the host stays reachable, which is the
	// whole point — killing the process instead would point every sandbox's
	// baked-in proxy variables at a dead socket.
	s.writeRules(rulesDoc("tunnel",
		rule{Name: "up", Host: host, Ports: []int{port}, Mode: "tunnel"},
	))
	s.reload()

	before := up.Count()

	// The connection now tunnels, so the proxy no longer terminates TLS. The
	// client's own trust store does not know the upstream's self-signed
	// certificate, so its handshake fails — which is itself the proof that
	// interception stopped.
	if _, err := s.client().Get(fmt.Sprintf("https://%s/x", hostPort(host, port))); err == nil {
		t.Log("the tunnelled request completed; checking that nothing was injected")
	}

	// However the client fared, no newly injected request reached the upstream.
	for _, req := range up.Requests()[before:] {
		if req.Header.Get("Authorization") != "" {
			t.Error("a credential was still injected after the all-tunnel kill switch")
		}
	}

	// And the CONNECT itself still succeeds: the network keeps working.
	conn, resp := s.connectKeepingConn(hostPort(host, port))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("CONNECT status = %d, want 200 — the kill switch must not break connectivity", resp.StatusCode)
	}
	_ = conn.Close()
}

// TestReloadKeepsALiveConnectionOnItsRuleset pins the take-once-per-connection
// contract in rules.Store.Engine.
//
// The MITM handler re-took the engine on every request, so a SIGHUP landing
// mid-connection left the CONNECT decision and the per-request decisions on
// that same connection evaluating different rulesets. The README documents the
// consequence for the kill switch: established connections drain under the
// rules their CONNECT was decided against.
func TestReloadKeepsALiveConnectionOnItsRuleset(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	intercept := rule{Name: "up", Host: host, Ports: []int{port}, Mode: "intercept"}
	s := startStack(t, stackOptions{
		Rules:      rulesDoc("deny", intercept),
		AllowAddrs: []string{hostPort(host, port)},
		UpstreamCA: up.CertPEM(t),
	})

	// One client, so its transport pools the intercepted connection and the
	// second request travels over the same one.
	client := s.client()
	target := fmt.Sprintf("https://%s/thing", hostPort(host, port))

	if resp, _ := requestThroughProxy(t, client, http.MethodGet, target); resp.StatusCode != http.StatusOK {
		t.Fatalf("before reload: status = %d, want 200 (logs:\n%s)", resp.StatusCode, s.Logs())
	}

	s.writeRules(rulesDoc("deny", intercept,
		rule{Name: "no-things", Host: host, Ports: []int{port}, Path: "/thing", Mode: "deny"}))
	s.reload()

	if resp, _ := requestThroughProxy(t, client, http.MethodGet, target); resp.StatusCode != http.StatusOK {
		t.Errorf("on the live connection: status = %d, want 200 — it must keep the ruleset its CONNECT was decided under", resp.StatusCode)
	}

	// A fresh connection takes the new snapshot.
	if resp, _ := requestThroughProxy(t, s.client(), http.MethodGet, target); resp.StatusCode != http.StatusForbidden {
		t.Errorf("on a new connection: status = %d, want 403 from the reloaded deny rule (logs:\n%s)", resp.StatusCode, s.Logs())
	}
}

// TestTokenRotationTakesEffectOnReload is the compromise response documented in
// docs/security-model.md: `token rotate`, re-provision, SIGHUP.
//
// The token used to be captured once at startup, so a reload left the leaked
// value working and 407'd every re-provisioned sandbox — the procedure did
// nothing until someone restarted the process.
func TestTokenRotationTakesEffectOnReload(t *testing.T) {
	s := startStack(t, stackOptions{Rules: rulesDoc("tunnel")})

	old := s.token
	fresh := s.rotateToken()
	if fresh == old {
		t.Fatal("token rotate returned the same token")
	}

	s.reload()

	// The leaked token no longer authenticates.
	conn := dialProxy(t, s)
	writeRaw(t, conn, fmt.Sprintf("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: %s\r\n\r\n",
		basicCredential(old)))
	resp, err := readResponse(conn)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Errorf("old token: status = %d, want 407 (logs:\n%s)", resp.StatusCode, s.Logs())
	}

	// And a re-provisioned sandbox works. The dial fails because nothing is
	// listening on example.com here, so the proof is that the refusal is no
	// longer an authentication failure.
	fresh2 := dialProxy(t, s)
	writeRaw(t, fresh2, fmt.Sprintf("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: %s\r\n\r\n",
		basicCredential(fresh)))
	freshResp, err := readResponse(fresh2)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if freshResp.StatusCode == http.StatusProxyAuthRequired {
		t.Errorf("new token: status = 407, want the rotated token to be accepted (logs:\n%s)", s.Logs())
	}

	// The dashboard follows the same token.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, s.dashURL("/api/rules"), nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+old)
	dashResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("dashboard request: %v", err)
	}
	defer func() { _ = dashResp.Body.Close() }()
	if dashResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("dashboard with the old token: status = %d, want 401", dashResp.StatusCode)
	}
}

// TestEnvCredentialReload covers the other half of a SIGHUP: an edited
// env_credentials block.
//
// The resolver's cache was cleared on reload, which implies credential edits
// take effect, but the env source itself was built once at startup — so a
// newly added entry stayed unresolvable until a restart.
func TestEnvCredentialReload(t *testing.T) {
	up := newUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny", rule{
			Name: "up", Host: host, Ports: []int{port}, Mode: "intercept",
			Inject: inject(map[string]string{"Authorization": "Bearer ${cred.tok}"}),
		}),
		AllowAddrs: []string{hostPort(host, port)},
		Env:        map[string]string{"TOK": "SENTINEL-RELOADED"},
	})

	// No env_credentials entry yet: the reference cannot resolve.
	resp, _ := requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("http://%s/x", hostPort(host, port)))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("before reload: status = %d, want 403 for an unresolvable credential", resp.StatusCode)
	}

	s.writeConfig(func(cfg map[string]any) {
		cfg["env_credentials"] = map[string]any{
			"tok": map[string]any{"var": "TOK", "hosts": []string{host}},
		}
	})
	s.reload()

	resp, _ = requestThroughProxy(t, s.client(), http.MethodGet,
		fmt.Sprintf("http://%s/x", hostPort(host, port)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("after reload: status = %d, want 200 (logs:\n%s)", resp.StatusCode, s.Logs())
	}
	if got := up.Last(t).Header.Get("Authorization"); got != "Bearer SENTINEL-RELOADED" {
		t.Errorf("Authorization = %q, want the newly configured credential injected", got)
	}
}

// TestShutdownDrainsTunnel is V-25 / AC-7: a tunnel with an in-flight transfer,
// interrupted by SIGTERM, is drained rather than severed instantly.
//
// The assertion has both bounds on purpose. An upper bound alone passes whether
// the tunnel drained or was cut immediately, which is exactly the regression
// this is meant to catch: http.Server.Shutdown does not see hijacked
// connections, so without explicit tracking the drain window elapses instantly
// and the documented behaviour is a promise the code does not keep.
func TestShutdownDrainsTunnel(t *testing.T) {
	up := newTLSUpstream(t)
	host, port := up.HostPort(t)

	s := startStack(t, stackOptions{
		Rules:      rulesDoc("tunnel", rule{Name: "up", Host: host, Ports: []int{port}, Mode: "tunnel"}),
		AllowAddrs: []string{hostPort(host, port)},
	})

	conn, resp := s.connectKeepingConn(hostPort(host, port))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	// Hold the tunnel open for a measurable stretch after the signal, then
	// close it. A shutdown that ignores hijacked relays returns well before
	// this fires.
	const holdFor = 2 * time.Second
	go func() {
		time.Sleep(holdFor)
		_ = conn.Close()
	}()

	start := time.Now()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}

	exited := make(chan struct{})
	go func() {
		_, _ = s.cmd.Process.Wait()
		close(exited)
	}()

	select {
	case <-exited:
		elapsed := time.Since(start)
		// Lower bound: the open tunnel was actually waited on.
		if elapsed < holdFor {
			t.Errorf("shutdown took %v, less than the %v the tunnel was held open: hijacked relays were severed rather than drained",
				elapsed, holdFor)
		}
		// Upper bound: the documented window still caps it, so launchd can
		// restart the process.
		if elapsed > 15*time.Second {
			t.Errorf("shutdown took %v, beyond the documented 10s drain window", elapsed)
		}
		t.Logf("exited %v after SIGTERM with a tunnel held open for %v", elapsed, holdFor)
	case <-time.After(25 * time.Second):
		t.Fatal("the process did not exit within 25s of SIGTERM with a tunnel open")
	}

	logs := s.Logs()
	if !strings.Contains(logs, "shutting down") {
		t.Errorf("shutdown should be logged:\n%s", logs)
	}
}
