//go:build e2e

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStrictRoleBoundaryAndPublicDashboardEndpoints(t *testing.T) {
	s := startStack(t, stackOptions{Rules: rulesDoc("tunnel")})

	if got := dashboardAuthStatus(t, s, s.adminToken); got != http.StatusOK {
		t.Fatalf("admin dashboard status = %d, want 200", got)
	}
	if got := dashboardAuthStatus(t, s, s.agentToken); got != http.StatusFound {
		t.Fatalf("agent dashboard status = %d, want 302", got)
	}
	for _, path := range []string{
		"/dashboard", "/dashboard/", "/dashboard/app.js", "/dashboard/styles.css", "/dashboard/favicon.svg",
		"/dashboard/api/audit", "/dashboard/api/rules", "/dashboard/api/credentials", "/dashboard/api/events",
	} {
		request, err := http.NewRequest(http.MethodGet, s.dashURL(path), nil)
		if err != nil {
			t.Fatalf("build request for %s: %v", path, err)
		}
		request.Header.Set("Authorization", "Bearer "+s.agentToken)
		response, err := noRedirectClient().Do(request)
		if err != nil {
			t.Fatalf("agent request to %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusFound {
			t.Fatalf("agent request to %s status = %d, want 302", path, response.StatusCode)
		}
		if response.Header.Get("Location") != "/dashboard/unauthorized" {
			t.Fatalf("agent request to %s Location = %q, want /dashboard/unauthorized", path, response.Header.Get("Location"))
		}
	}
	assertProxyHeaderStatus(t, s, "Bearer "+s.adminToken, http.StatusProxyAuthRequired)
	assertProxyHeaderAccepted(t, s, "Bearer "+s.agentToken)
	assertProxyHeaderStatus(t, s, basicCredential(s.adminToken), http.StatusProxyAuthRequired)

	for _, tt := range []struct {
		path string
		want int
	}{
		{path: "/", want: http.StatusFound},
		{path: "/healthz", want: http.StatusOK},
		{path: "/ca.pem", want: http.StatusOK},
		{path: "/dashboard/unauthorized", want: http.StatusOK},
	} {
		response, err := noRedirectClient().Get(s.dashURL(tt.path))
		if err != nil {
			t.Fatalf("GET %s: %v", tt.path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != tt.want {
			t.Fatalf("GET %s status = %d, want %d", tt.path, response.StatusCode, tt.want)
		}
	}
}

func TestDashboardCookieBootstrapUsesOnlyAdminCredential(t *testing.T) {
	s := startStack(t, stackOptions{})
	client := noRedirectClient()
	response, err := client.Get(s.dashURL("/dashboard/?token=" + s.adminToken))
	if err != nil {
		t.Fatalf("admin bootstrap: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || len(response.Cookies()) != 1 {
		t.Fatalf("admin bootstrap status/cookies = %d/%d, want 302/1", response.StatusCode, len(response.Cookies()))
	}
	cookie := response.Cookies()[0]
	if cookie.Value != s.adminToken {
		t.Fatal("dashboard cookie did not contain the admin credential")
	}
	if cookie.Value == s.agentToken {
		t.Fatal("dashboard cookie contained the agent credential")
	}
	cookieRequest, _ := http.NewRequest(http.MethodGet, s.dashURL("/dashboard/api/rules"), nil)
	cookieRequest.AddCookie(cookie)
	cookieResponse, err := client.Do(cookieRequest)
	if err != nil {
		t.Fatal("admin cookie request failed")
	}
	_ = cookieResponse.Body.Close()
	if cookieResponse.StatusCode != http.StatusOK {
		t.Fatalf("admin cookie status = %d, want 200", cookieResponse.StatusCode)
	}
	agentCookieRequest, _ := http.NewRequest(http.MethodGet, s.dashURL("/dashboard/api/rules"), nil)
	agentCookieRequest.AddCookie(&http.Cookie{Name: cookie.Name, Value: s.agentToken})
	agentCookieResponse, err := client.Do(agentCookieRequest)
	if err != nil {
		t.Fatal("agent-valued cookie request failed")
	}
	_ = agentCookieResponse.Body.Close()
	if agentCookieResponse.StatusCode != http.StatusFound {
		t.Fatalf("agent-valued cookie status = %d, want 302", agentCookieResponse.StatusCode)
	}

	agentResponse, err := client.Get(s.dashURL("/dashboard/?token=" + s.agentToken))
	if err != nil {
		t.Fatalf("agent bootstrap: %v", err)
	}
	defer agentResponse.Body.Close()
	if agentResponse.StatusCode != http.StatusFound || len(agentResponse.Cookies()) != 0 {
		t.Fatalf("agent bootstrap status/cookies = %d/%d, want 302/0", agentResponse.StatusCode, len(agentResponse.Cookies()))
	}
}

func TestLegacyMigrationAndNonTerminalStartupConfineCredentials(t *testing.T) {
	legacy := strings.Repeat("a", 64)
	s := startStack(t, stackOptions{LegacyToken: "\n" + legacy + "\t"})
	if s.agentToken != legacy {
		t.Fatal("legacy credential was not preserved as the canonical agent credential")
	}
	if s.adminToken == legacy {
		t.Fatal("migration reused the legacy credential as the admin credential")
	}
	if _, err := os.Stat(filepath.Join(s.configDir, "auth-token")); !os.IsNotExist(err) {
		t.Fatal("migration-only legacy path was not retired")
	}
	logs := []byte(s.Logs())
	if bytes.Contains(logs, []byte(s.agentToken)) {
		t.Fatal("non-terminal daemon output contained the agent credential")
	}
	if bytes.Contains(logs, []byte(s.adminToken)) {
		t.Fatal("non-terminal daemon output contained the admin credential")
	}
}

func TestAdminRotationLeavesAuthenticatedSSEOpen(t *testing.T) {
	s := startStack(t, stackOptions{})
	bootstrap, err := noRedirectClient().Get(s.dashURL("/dashboard/?token=" + s.adminToken))
	if err != nil {
		t.Fatal("dashboard cookie bootstrap failed")
	}
	oldCookie := bootstrap.Cookies()[0]
	_ = bootstrap.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.dashURL("/dashboard/api/events"), nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+s.adminToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read SSE greeting: %v", err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read SSE greeting terminator: %v", err)
	}

	oldAdmin := s.adminToken
	freshAdmin := s.rotateToken("admin")
	s.reload()
	if got := dashboardAuthStatus(t, s, oldAdmin); got != http.StatusFound {
		t.Fatalf("old admin status = %d, want 302", got)
	}
	if got := dashboardAuthStatus(t, s, freshAdmin); got != http.StatusOK {
		t.Fatalf("new admin status = %d, want 200", got)
	}
	oldCookieRequest, _ := http.NewRequest(http.MethodGet, s.dashURL("/dashboard/api/rules"), nil)
	oldCookieRequest.AddCookie(oldCookie)
	oldCookieResponse, err := noRedirectClient().Do(oldCookieRequest)
	if err != nil {
		t.Fatal("old admin cookie request failed")
	}
	_ = oldCookieResponse.Body.Close()
	if oldCookieResponse.StatusCode != http.StatusFound {
		t.Fatalf("old admin cookie status = %d, want 302", oldCookieResponse.StatusCode)
	}

	assertProxyHeaderStatus(t, s, "", http.StatusProxyAuthRequired)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("existing SSE did not continue after admin rotation: %v", err)
	}
	if !strings.HasPrefix(line, "event: audit") {
		t.Fatalf("existing SSE line = %q, want audit event", line)
	}
}

func TestAgentRotationLeavesEstablishedTunnelOpen(t *testing.T) {
	upstream := newUpstream(t)
	host, port := upstream.HostPort(t)
	target := hostPort(host, port)
	s := startStack(t, stackOptions{
		Rules:      rulesDoc("deny", rule{Name: "tunnel", Host: host, Ports: []int{port}, Mode: "tunnel"}),
		AllowAddrs: []string{target},
	})
	oldAgent := s.agentToken
	connection, response := s.connectKeepingConn(target)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("initial CONNECT status = %d, want 200", response.StatusCode)
	}
	reader := bufio.NewReader(connection)
	requestAndReadTunnel(t, connection, reader, target, "/before")

	freshAgent := s.rotateToken("agent")
	s.reload()
	assertConnectAuthStatus(t, s, oldAgent, http.StatusProxyAuthRequired)
	assertConnectAccepted(t, s, freshAgent)
	requestAndReadTunnel(t, connection, reader, target, "/after")
}

func TestAgentRotationLeavesEstablishedMITMConnectionOpen(t *testing.T) {
	upstream := newTLSUpstream(t)
	host, port := upstream.HostPort(t)
	target := hostPort(host, port)
	s := startStack(t, stackOptions{
		Rules:      rulesDoc("deny", rule{Name: "intercept", Host: host, Ports: []int{port}, Mode: "intercept"}),
		AllowAddrs: []string{target},
		UpstreamCA: upstream.CertPEM(t),
	})
	client := s.client()
	if response, _ := requestThroughProxy(t, client, http.MethodGet, upstream.URL+"/before"); response.StatusCode != http.StatusOK {
		t.Fatalf("initial MITM response status = %d, want 200", response.StatusCode)
	}
	oldAgent := s.agentToken
	freshAgent := s.rotateToken("agent")
	s.reload()
	assertConnectAuthStatus(t, s, oldAgent, http.StatusProxyAuthRequired)
	assertConnectAccepted(t, s, freshAgent)
	if response, _ := requestThroughProxy(t, client, http.MethodGet, upstream.URL+"/after"); response.StatusCode != http.StatusOK {
		t.Fatalf("reused MITM response status = %d, want 200", response.StatusCode)
	}
}

func requestAndReadTunnel(t *testing.T, connection io.Writer, reader *bufio.Reader, host, path string) {
	t.Helper()
	if _, err := fmt.Fprintf(connection, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", path, host); err != nil {
		t.Fatalf("write tunneled request: %v", err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read tunneled response: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tunneled response status = %d, want 200", response.StatusCode)
	}
	_, _ = io.Copy(io.Discard, response.Body)
}

func assertProxyHeaderStatus(t *testing.T, s *stack, header string, want int) {
	t.Helper()
	connection := dialProxy(t, s)
	writeRaw(t, connection, fmt.Sprintf("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: %s\r\n\r\n", header))
	response, err := readResponse(connection)
	if err != nil {
		t.Fatalf("read proxy response: %v", err)
	}
	if response.StatusCode != want {
		t.Fatalf("proxy status = %d, want %d", response.StatusCode, want)
	}
}

func assertProxyHeaderAccepted(t *testing.T, s *stack, header string) {
	t.Helper()
	connection := dialProxy(t, s)
	writeRaw(t, connection, fmt.Sprintf("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nProxy-Authorization: %s\r\n\r\n", header))
	response, err := readResponse(connection)
	if err != nil {
		t.Fatalf("read proxy response: %v", err)
	}
	if response.StatusCode == http.StatusProxyAuthRequired {
		t.Fatal("agent credential was rejected by the proxy")
	}
}

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
