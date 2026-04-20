//go:build e2e

package e2e_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var gatewayBinary string

func TestMain(m *testing.M) {
	// Build the agent-gateway binary once for all tests.
	tmp, err := os.MkdirTemp("", "agent-gateway-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "agent-gateway")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/agent-gateway")
	cmd.Dir = filepath.Join(mustFindModuleRoot(), "agent-gateway")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build agent-gateway: %v\n", err)
		os.Exit(1)
	}
	gatewayBinary = bin

	os.Exit(m.Run())
}

// mustFindModuleRoot walks up from the working directory to find the go.work file.
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

// freePort returns a free TCP port by briefly binding to :0.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// --- Self-signed upstream CA + server ---

// upstreamCA holds a self-signed CA used only for the mock upstream.
type upstreamCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	pemData []byte
}

// newUpstreamCA generates a fresh self-signed CA for the upstream mock server.
func newUpstreamCA(t *testing.T) *upstreamCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate upstream CA key: %v", err)
	}
	serial, err := randSerial()
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "e2e-upstream-ca"},
		NotBefore:             now,
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create upstream CA cert: %v", err)
	}
	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("parse upstream CA cert: %v", err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	return &upstreamCA{cert: cert, key: key, pemData: pemData}
}

// tlsConfigForServer returns a *tls.Config with a leaf cert signed by this CA,
// valid for localhost (DNS) and 127.0.0.1 (IP).
func (ca *upstreamCA) tlsConfigForServer(t *testing.T) *tls.Config {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	serial, err := randSerial()
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now,
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &leafKey.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create upstream server cert: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{derBytes},
				PrivateKey:  leafKey,
			},
		},
	}
}

func randSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, max)
}

// startMockUpstream starts an HTTPS httptest.Server with the given handler.
// Its certificate is signed by upCA so the daemon will trust it when upCA's PEM
// is provided via SSL_CERT_FILE. Returns the server; cleanup is registered.
func startMockUpstream(t *testing.T, upCA *upstreamCA, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = upCA.tlsConfigForServer(t)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// --- TestStack ---

// TestStack holds handles to a running agent-gateway subprocess and its
// associated mock upstream.
type TestStack struct {
	ProxyAddr     string
	DashboardAddr string
	UpstreamURL   string
	CAPath        string       // path to the daemon's ca.pem
	CAPEM         []byte       // PEM bytes of the daemon's CA cert
	AgentClient   *http.Client // pre-configured to proxy through agent-gateway

	// XDG directories — exposed so helpers can run CLI subcommands in the
	// same environment as the daemon.
	CfgHome  string // XDG_CONFIG_HOME for this stack
	DataHome string // XDG_DATA_HOME for this stack
	CfgPath  string // path to config.hcl
	RulesDir string // path to the rules.d directory
}

func newTestStack(t *testing.T, handler http.Handler) *TestStack {
	t.Helper()

	// 1. Generate an upstream CA and start the mock upstream.
	upCA := newUpstreamCA(t)
	upstream := startMockUpstream(t, upCA, handler)

	// 2. Choose ephemeral ports for proxy and dashboard.
	proxyPort := freePort(t)
	dashPort := freePort(t)

	// 3. Create XDG temp dirs.
	cfgHome := t.TempDir()
	dataHome := t.TempDir()

	// 4. Write SSL_CERT_FILE — the upstream CA PEM so the daemon trusts upstream.
	sslCertFile := filepath.Join(t.TempDir(), "ssl-certs.pem")
	if err := os.WriteFile(sslCertFile, upCA.pemData, 0o644); err != nil {
		t.Fatalf("write SSL_CERT_FILE: %v", err)
	}

	// 5. Write config.hcl pointing at ephemeral ports.
	cfgDir := filepath.Join(cfgHome, "agent-gateway")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	rulesDir := filepath.Join(cfgDir, "rules.d")
	if err := os.MkdirAll(rulesDir, 0o750); err != nil {
		t.Fatalf("mkdir rules dir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.hcl")
	cfgContent := fmt.Sprintf(`
proxy {
  listen = "127.0.0.1:%d"
}
dashboard {
  listen       = "127.0.0.1:%d"
  open_browser = false
}
`, proxyPort, dashPort)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("write config.hcl: %v", err)
	}

	// 6. Start daemon subprocess.
	daemonCmd := exec.Command(gatewayBinary, "serve")
	daemonCmd.Stdout = os.Stdout
	daemonCmd.Stderr = os.Stderr
	daemonCmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+cfgHome,
		"XDG_DATA_HOME="+dataHome,
		"SSL_CERT_FILE="+sslCertFile,
	)
	if err := daemonCmd.Start(); err != nil {
		t.Fatalf("start agent-gateway: %v", err)
	}
	t.Cleanup(func() {
		_ = daemonCmd.Process.Signal(os.Interrupt)
		_ = daemonCmd.Wait()
	})

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	dashAddr := fmt.Sprintf("127.0.0.1:%d", dashPort)

	// 7. Wait for daemon to bind (poll dashboard).
	deadline := time.Now().Add(15 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + dashAddr)
		if err == nil {
			resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("agent-gateway not ready within 15s")
	}

	// 8. Read the generated CA PEM from the data dir.
	caPath := filepath.Join(dataHome, "agent-gateway", "ca.pem")
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read ca.pem: %v", err)
	}

	// Rewrite the upstream URL to use "localhost" instead of "127.0.0.1" so that
	// the proxy can MITM the connection (IP literals always tunnel per §6).
	// The upstream TLS cert is valid for both localhost (DNS SAN) and 127.0.0.1
	// (IP SAN), so this substitution is safe.
	upstreamURL := strings.Replace(upstream.URL, "127.0.0.1", "localhost", 1)

	stack := &TestStack{
		ProxyAddr:     proxyAddr,
		DashboardAddr: dashAddr,
		UpstreamURL:   upstreamURL,
		CAPath:        caPath,
		CAPEM:         caPEM,
		CfgHome:       cfgHome,
		DataHome:      dataHome,
		CfgPath:       cfgPath,
		RulesDir:      filepath.Join(cfgDir, "rules.d"),
	}

	// 9. Write a catch-all MITM rule (last in sort order) for all agents,
	// register a default agent, and build AgentClient authenticated as that agent.
	// The wildcard rule ensures existing tests MITM through localhost (IP literals
	// always tunnel, so the hostname rewrite above is required).
	// Tests that need fine-grained scope control should overwrite or remove this
	// file before writing their own rules.
	stack.writeRule(t, "zz-allow-all.hcl", `
rule "allow-all" {
  verdict = "allow"
  match {
    host = "**"
  }
}
`)
	defaultToken := stack.agentAdd(t, "default")
	// Wait for the SIGHUP-triggered rules + registry reload to settle.
	time.Sleep(300 * time.Millisecond)
	stack.AgentClient = stack.agentHTTPClient(t, caPEM, defaultToken)

	return stack
}

// xdgEnv returns the XDG environment variables for running CLI subcommands
// in the same environment as the daemon.
func (s *TestStack) xdgEnv() []string {
	return append(os.Environ(),
		"XDG_CONFIG_HOME="+s.CfgHome,
		"XDG_DATA_HOME="+s.DataHome,
	)
}

// writeRule writes content to <RulesDir>/<filename>.
func (s *TestStack) writeRule(t *testing.T, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.RulesDir, filename), []byte(content), 0o600); err != nil {
		t.Fatalf("writeRule %q: %v", filename, err)
	}
}

// setSecret calls "agent-gateway secret add <name> --host <glob> ..." piping
// value via stdin. At least one host is required; use "**" for tests that
// don't care about scoping.
// XDG env vars are forwarded so the CLI writes to the daemon's data directory.
func (s *TestStack) setSecret(t *testing.T, name, value string, hosts ...string) {
	t.Helper()
	if len(hosts) == 0 {
		t.Fatalf("setSecret %q: at least one host glob required", name)
	}
	args := []string{"secret", "add", name}
	for _, h := range hosts {
		args = append(args, "--host", h)
	}
	cmd := exec.Command(gatewayBinary, args...)
	cmd.Stdin = strings.NewReader(value)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = s.xdgEnv()
	if err := cmd.Run(); err != nil {
		t.Fatalf("secret add %q: %v", name, err)
	}
}

// setAgentSecret calls "agent-gateway secret add --agent <agent> <name>
// --host <glob> ..." piping value via stdin, writing an agent-scoped secret.
func (s *TestStack) setAgentSecret(t *testing.T, agent, name, value string, hosts ...string) {
	t.Helper()
	if len(hosts) == 0 {
		t.Fatalf("setAgentSecret %q/%q: at least one host glob required", agent, name)
	}
	args := []string{"secret", "add", "--agent", agent, name}
	for _, h := range hosts {
		args = append(args, "--host", h)
	}
	cmd := exec.Command(gatewayBinary, args...)
	cmd.Stdin = strings.NewReader(value)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = s.xdgEnv()
	if err := cmd.Run(); err != nil {
		t.Fatalf("secret add --agent %q %q: %v", agent, name, err)
	}
}

// rulesReload calls "agent-gateway rules reload" to signal the daemon via SIGHUP.
func (s *TestStack) rulesReload(t *testing.T) {
	t.Helper()
	cmd := exec.Command(gatewayBinary, "rules", "reload")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = s.xdgEnv()
	if err := cmd.Run(); err != nil {
		t.Fatalf("rules reload: %v", err)
	}
}

// agentAdd registers a new agent via the CLI and returns the minted token.
// The CLI writes to the daemon's data directory via XDG env vars, then sends
// SIGHUP so the daemon reloads its in-memory agent prefix map.
func (s *TestStack) agentAdd(t *testing.T, name string) string {
	t.Helper()
	var buf strings.Builder
	cmd := exec.Command(gatewayBinary, "agent", "add", name)
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	cmd.Env = s.xdgEnv()
	if err := cmd.Run(); err != nil {
		t.Fatalf("agent add %q: %v", name, err)
	}

	// The CLI prints:
	//   token: <TOKEN>
	//   HTTPS_PROXY=http://x:<TOKEN>@<ADDR>
	//   HTTP_PROXY=...
	// Extract the token from the first line.
	for _, line := range strings.Split(buf.String(), "\n") {
		if tok, ok := strings.CutPrefix(line, "token: "); ok {
			return strings.TrimSpace(tok)
		}
	}
	t.Fatalf("agent add %q: could not parse token from output: %q", name, buf.String())
	return ""
}

// agentHTTPClient builds an http.Client that proxies through this stack's
// proxy and authenticates as the agent identified by token. The client trusts
// the gateway CA (for MITM TLS verification) but not the upstream's own CA,
// so a tunnelled connection to the TLS upstream would fail cert verification
// (the upstream uses a different CA than the gateway).
func (s *TestStack) agentHTTPClient(t *testing.T, caPEM []byte, token string) *http.Client {
	t.Helper()

	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to add agent-gateway CA to root pool")
	}

	// Encode Proxy-Authorization: Basic base64("x:<token>")
	// The proxy's parseAuth extracts the token from the password field.
	proxyURL, _ := url.Parse("http://x:" + token + "@" + s.ProxyAddr)

	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: rootPool,
			},
		},
	}
}
