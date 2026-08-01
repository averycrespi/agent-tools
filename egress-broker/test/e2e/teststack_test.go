//go:build e2e

// Package e2e drives the real egress-broker binary as a subprocess.
//
// Everything here talks to the tool the way a sandboxed agent would: over the
// proxy port, with HTTPS_PROXY semantics and a client that trusts the test CA.
// Nothing reaches into internal packages, so a test passing here means the
// shipped binary behaves, not that a unit was wired correctly.
//
// Credentials always come from env_credentials, never the keychain, so the
// suite cannot touch a developer's real secrets or prompt for keychain access.
package e2e_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

var brokerBinary string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "egress-broker-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	bin := filepath.Join(tmp, "egress-broker")
	build := exec.Command("go", "build", "-o", bin, "./cmd/egress-broker")
	build.Dir = filepath.Join(mustFindModuleRoot(), "egress-broker")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build egress-broker: %v\n", err)
		os.Exit(1)
	}
	brokerBinary = bin

	os.Exit(m.Run())
}

func mustFindModuleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("could not find go.work in any parent directory")
		}
		dir = parent
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a free port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// stackOptions configures a test stack.
type stackOptions struct {
	// Rules is the rules document written before start.
	Rules map[string]any
	// EnvCredentials are written into config.json.
	EnvCredentials map[string]any
	// Env holds extra environment variables for the broker process, which is
	// how env_credentials values reach it.
	Env map[string]string
	// UpstreamCA is a PEM certificate the proxy should trust when it dials
	// upstream.
	//
	// The proxy verifies upstream certificates against the system trust store
	// with no InsecureSkipVerify (D12), which is the behaviour we want and
	// which a self-signed mock upstream necessarily fails. Go's system pool
	// honours SSL_CERT_FILE on Unix, so pointing that at the mock's own
	// certificate is enough — no code change and no verification-weakening
	// switch.
	UpstreamCA []byte
	// AllowAddrs are exact "host:port" targets the address guard should skip.
	//
	// Every mock upstream a test can start listens on loopback, which the
	// guard refuses by design, so reaching one requires naming it explicitly.
	// Refusal tests deliberately leave this empty, so the guard is still under
	// test rather than switched off for the whole suite.
	AllowAddrs []string
}

// stack is a running broker plus everything needed to talk to it.
type stack struct {
	t          *testing.T
	proxyPort  int
	dashPort   int
	configDir  string
	dataDir    string
	token      string
	cmd        *exec.Cmd
	logPath    string
	caPool     *x509.CertPool
	rulesPath  string
	configPath string
}

func startStack(t *testing.T, opts stackOptions) *stack {
	t.Helper()

	configHome := t.TempDir()
	dataHome := t.TempDir()
	configDir := filepath.Join(configHome, "egress-broker")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	s := &stack{
		t:          t,
		proxyPort:  freePort(t),
		dashPort:   freePort(t),
		configDir:  configDir,
		dataDir:    filepath.Join(dataHome, "egress-broker"),
		rulesPath:  filepath.Join(configDir, "rules.json"),
		configPath: filepath.Join(configDir, "config.json"),
	}

	cfg := map[string]any{
		"proxy":     map[string]any{"host": "127.0.0.1", "port": s.proxyPort},
		"dashboard": map[string]any{"host": "127.0.0.1", "port": s.dashPort},
		"log":       map[string]any{"level": "debug"},
	}
	if len(opts.EnvCredentials) > 0 {
		cfg["env_credentials"] = opts.EnvCredentials
	}
	writeJSON(t, s.configPath, cfg)

	rules := opts.Rules
	if rules == nil {
		rules = map[string]any{"fallthrough": "tunnel", "rules": []any{}}
	}
	writeJSON(t, s.rulesPath, rules)

	logFile, err := os.CreateTemp(t.TempDir(), "serve-*.log")
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}
	s.logPath = logFile.Name()

	cmd := exec.Command(brokerBinary, "serve")
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+configHome,
		"XDG_DATA_HOME="+dataHome,
	)
	if len(opts.UpstreamCA) > 0 {
		caPath := filepath.Join(t.TempDir(), "upstream-ca.pem")
		if err := os.WriteFile(caPath, opts.UpstreamCA, 0o600); err != nil {
			t.Fatalf("writing the upstream CA: %v", err)
		}
		cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+caPath)
	}
	if len(opts.AllowAddrs) > 0 {
		cmd.Env = append(cmd.Env, "EGRESS_BROKER_TEST_ALLOW_ADDRS="+strings.Join(opts.AllowAddrs, ","))
	}
	for k, v := range opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the broker: %v", err)
	}
	s.cmd = cmd

	t.Cleanup(func() {
		s.stop()
		_ = logFile.Close()
	})

	s.waitReady()
	s.token = s.readToken()
	s.caPool = s.fetchCAPool()
	return s
}

// waitReady polls /healthz, which is exactly what an external liveness probe
// would do (AC-19).
func (s *stack) waitReady() {
	s.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.dashURL("/healthz")) //nolint:noctx // short-lived readiness poll
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if s.cmd.ProcessState != nil {
			s.t.Fatalf("the broker exited during startup:\n%s", s.Logs())
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("the broker never became ready:\n%s", s.Logs())
}

func (s *stack) readToken() string {
	s.t.Helper()
	data, err := os.ReadFile(filepath.Join(s.configDir, "auth-token"))
	if err != nil {
		s.t.Fatalf("reading the auth token: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// fetchCAPool retrieves the CA over the unauthenticated endpoint, the same way
// provisioning would obtain it.
func (s *stack) fetchCAPool() *x509.CertPool {
	s.t.Helper()
	resp, err := http.Get(s.dashURL("/ca.pem")) //nolint:noctx // test helper
	if err != nil {
		s.t.Fatalf("fetching the CA: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	pem, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatalf("reading the CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		s.t.Fatalf("the CA endpoint did not return a usable certificate:\n%s", pem)
	}
	return pool
}

func (s *stack) dashURL(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", s.dashPort, path)
}

func (s *stack) proxyAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(s.proxyPort))
}

// proxyURL is the value a sandbox would put in HTTPS_PROXY, with the token
// embedded as Basic credentials.
func (s *stack) proxyURL() *url.URL {
	u, err := url.Parse("http://x:" + s.token + "@" + s.proxyAddr())
	if err != nil {
		s.t.Fatalf("building the proxy URL: %v", err)
	}
	return u
}

// client returns an HTTP client that routes through the proxy and trusts the
// test CA, which is what a provisioned sandbox looks like.
func (s *stack) client() *http.Client {
	proxyURL := s.proxyURL()
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: s.caPool, MinVersion: tls.VersionTLS12},
		},
	}
}

// unauthenticatedClient routes through the proxy with no credentials.
func (s *stack) unauthenticatedClient() *http.Client {
	u, _ := url.Parse("http://" + s.proxyAddr())
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(u),
			TLSClientConfig: &tls.Config{RootCAs: s.caPool, MinVersion: tls.VersionTLS12},
		},
	}
}

// connect performs a raw CONNECT and returns the proxy's response, without
// attempting a TLS handshake. It is how tunnel-level refusals are asserted.
func (s *stack) connect(target string) *http.Response {
	s.t.Helper()

	conn, err := net.DialTimeout("tcp", s.proxyAddr(), 5*time.Second)
	if err != nil {
		s.t.Fatalf("dialling the proxy: %v", err)
	}
	s.t.Cleanup(func() { _ = conn.Close() })

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		target, target, basicCredential(s.token))
	if _, err := conn.Write([]byte(req)); err != nil {
		s.t.Fatalf("writing CONNECT: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		s.t.Fatalf("set deadline: %v", err)
	}

	resp, err := readResponse(conn)
	if err != nil {
		s.t.Fatalf("reading the CONNECT response: %v", err)
	}
	return resp
}

// writeRules replaces rules.json without restarting.
func (s *stack) writeRules(doc map[string]any) {
	s.t.Helper()
	writeJSON(s.t, s.rulesPath, doc)
}

// writeRulesRaw writes arbitrary bytes, for the invalid-file reload case.
func (s *stack) writeRulesRaw(content string) {
	s.t.Helper()
	if err := os.WriteFile(s.rulesPath, []byte(content), 0o600); err != nil {
		s.t.Fatalf("writing rules: %v", err)
	}
}

// reload sends SIGHUP and waits for the broker to log that it finished.
func (s *stack) reload() {
	s.t.Helper()
	before := len(s.Logs())
	if err := s.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		s.t.Fatalf("sending SIGHUP: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.Logs()) > before {
			// Give the swap a moment to become visible to new connections.
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatalf("no log output after SIGHUP:\n%s", s.Logs())
}

// Logs returns everything the broker has written so far.
func (s *stack) Logs() string {
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *stack) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = s.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshalling %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// requestThroughProxy issues a request and returns the response plus its body.
func requestThroughProxy(t *testing.T, c *http.Client, method, rawURL string) (*http.Response, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("request to %s: %v", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	return resp, string(body)
}
