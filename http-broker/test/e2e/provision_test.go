//go:build e2e

package e2e_test

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedBundle writes a stand-in for the system CA bundle that update-ca-
// certificates has already merged the broker CA into.
func seedBundle(t *testing.T, home string) string {
	t.Helper()

	ca, err := os.ReadFile(filepath.Join(home, ".local", "share", "http-broker", "ca.pem"))
	if err != nil {
		t.Fatalf("reading the seeded CA: %v", err)
	}

	bundle := filepath.Join(t.TempDir(), "ca-certificates.crt")
	body := "-----BEGIN CERTIFICATE-----\nsome-other-root\n-----END CERTIFICATE-----\n" + string(ca)
	if err := os.WriteFile(bundle, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the bundle: %v", err)
	}
	return bundle
}

// runProvision runs the provisioning script against a temporary HOME, a mocked
// trust-store root and a mocked system bundle, returning the generated
// ~/.bashrc. The bundle is a parameter rather than seeded per call because its
// path appears in the generated block, so two runs must be given the same one
// to compare.
func runProvision(t *testing.T, home, caDest, bundle string) string {
	t.Helper()

	script := filepath.Join(mustFindModuleRoot(), "http-broker",
		"examples", "provision", "configure-http-broker.sh")

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"HTTP_BROKER_CA_DEST="+caDest,
		"HTTP_BROKER_CA_BUNDLE="+bundle,
		// A no-op stand-in for update-ca-certificates, which needs root.
		"HTTP_BROKER_UPDATE_CA=true",
		// No escalation: this writes into a temporary directory as the test
		// user. TestProvisionEscalatesForTheTrustStore covers the escalation.
		"HTTP_BROKER_SUDO=",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the provisioning script: %v\n%s", err, out)
	}

	// The CA must have been installed where the script said it would be.
	if _, err := os.Stat(caDest); err != nil {
		t.Errorf("the script did not install the CA to %s: %v", caDest, err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	if err != nil {
		t.Fatalf("reading the generated .bashrc: %v", err)
	}
	return string(data)
}

// seedProvisionHome writes the two files copy_paths is expected to ship in.
func seedProvisionHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	tokenPath := filepath.Join(home, ".config", "http-broker", "auth-token")
	caPath := filepath.Join(home, ".local", "share", "http-broker", "ca.pem")

	for _, p := range []string{tokenPath, caPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(tokenPath, []byte("test-token-value\n"), 0o600); err != nil {
		t.Fatalf("writing the token: %v", err)
	}
	if err := os.WriteFile(caPath, []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatalf("writing the CA: %v", err)
	}
	return home
}

// TestProvisionScript is V-18 / AC-15.
func TestProvisionScript(t *testing.T) {
	home := seedProvisionHome(t)
	caDest := filepath.Join(t.TempDir(), "ca-certificates", "http-broker.crt")

	bundle := seedBundle(t, home)

	first := runProvision(t, home, caDest, bundle)
	second := runProvision(t, home, caDest, bundle)

	// Idempotent: two runs leave exactly one marker block.
	const marker = "# >>> http-broker >>>"
	if got := strings.Count(second, marker); got != 1 {
		t.Errorf("after two runs the .bashrc has %d marker blocks, want exactly 1", got)
	}
	if first != second {
		t.Error("a second run produced different output; the block is not idempotent")
	}

	// AC-15 names every variable that must be set.
	required := []string{
		"HTTP_PROXY", "HTTPS_PROXY",
		"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE",
		"CURL_CA_BUNDLE", "GIT_SSL_CAINFO", "DENO_CERT",
		"NO_PROXY",
	}
	for _, name := range required {
		if !strings.Contains(second, name) {
			t.Errorf("the generated block does not set %s", name)
		}
	}

	// The NO_PROXY carve-out is what keeps mcp-broker and local-gomod-proxy
	// reachable; without it this tool becomes a single point of failure for
	// both.
	for _, carveOut := range []string{"host.lima.internal", "localhost", "127.0.0.1"} {
		if !strings.Contains(second, carveOut) {
			t.Errorf("NO_PROXY does not carve out %s", carveOut)
		}
	}

	// The script must not embed the token literally: it reads the file at
	// shell startup so rotation is picked up by re-provisioning alone.
	if strings.Contains(second, "test-token-value") {
		t.Error("the generated block embeds the token literally; it should read the file at shell startup")
	}
}

// TestProvisionTrustsTheSystemBundle pins where each CA variable points.
//
// The variables that runtimes treat as a replacement for their whole trust set
// must name the system bundle, which already contains the broker CA. Naming the
// single broker cert instead makes it the ONLY trusted CA, so every host
// presenting its real certificate fails verification — Python's ssl and
// requests both do exactly that, and under fallthrough "tunnel" that is all
// traffic. NODE_EXTRA_CA_CERTS is the exception: it appends, so it names the
// single cert.
func TestProvisionTrustsTheSystemBundle(t *testing.T) {
	home := seedProvisionHome(t)
	caDest := filepath.Join(t.TempDir(), "ca-certificates", "http-broker.crt")
	bundle := seedBundle(t, home)

	bashrc := runProvision(t, home, caDest, bundle)

	for _, name := range []string{
		"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE",
		"GIT_SSL_CAINFO", "DENO_CERT",
	} {
		if got := extractExport(t, bashrc, name); got != bundle {
			t.Errorf("%s = %q, want the system bundle %q — naming the single broker cert "+
				"makes it the only trusted CA", name, got, bundle)
		}
	}

	if got := extractExport(t, bashrc, "NODE_EXTRA_CA_CERTS"); got != caDest {
		t.Errorf("NODE_EXTRA_CA_CERTS = %q, want the single cert %q — it appends to "+
			"Node's built-ins", got, caDest)
	}
}

// TestProvisionFailsWhenTheBundleLacksTheCA covers the other half: trust now
// comes from the bundle, so a bundle the broker CA never reached would break
// interception everywhere, and only once a rule exists to reveal it.
func TestProvisionFailsWhenTheBundleLacksTheCA(t *testing.T) {
	home := seedProvisionHome(t)
	caDest := filepath.Join(t.TempDir(), "ca-certificates", "http-broker.crt")

	bundle := filepath.Join(t.TempDir(), "ca-certificates.crt")
	other := "-----BEGIN CERTIFICATE-----\nsome-other-root\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(bundle, []byte(other), 0o644); err != nil {
		t.Fatalf("writing the bundle: %v", err)
	}

	script := filepath.Join(mustFindModuleRoot(), "http-broker",
		"examples", "provision", "configure-http-broker.sh")

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"HTTP_BROKER_CA_DEST="+caDest,
		"HTTP_BROKER_CA_BUNDLE="+bundle,
		"HTTP_BROKER_UPDATE_CA=true",
		"HTTP_BROKER_SUDO=",
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the script should fail when the bundle does not contain the CA:\n%s", out)
	}
	if !strings.Contains(string(out), bundle) {
		t.Errorf("the error should name the bundle so the operator knows what to check:\n%s", out)
	}

	// A half-configured shell is worse than an unconfigured one: it would point
	// every runtime at a bundle that cannot verify an intercepted connection.
	if data, err := os.ReadFile(filepath.Join(home, ".bashrc")); err == nil {
		if strings.Contains(string(data), "HTTP_PROXY") {
			t.Error("the script wrote the shell block despite failing the bundle check")
		}
	}
}

// TestProvisionEscalatesForTheTrustStore pins that both privileged steps run
// through an escalation prefix. `sb` runs provisioning scripts as the
// unprivileged guest user, and /usr/local/share/ca-certificates is root-owned,
// so an unescalated install fails with permission denied on a real sandbox.
func TestProvisionEscalatesForTheTrustStore(t *testing.T) {
	home := seedProvisionHome(t)
	caDest := filepath.Join(t.TempDir(), "ca-certificates", "http-broker.crt")

	bin := t.TempDir()

	// A stand-in for sudo that records its arguments, then runs them.
	logPath := filepath.Join(bin, "sudo.log")
	sudoStub := filepath.Join(bin, "sudo-stub")
	stubBody := fmt.Sprintf("#!/bin/bash\nprintf '%%s\\n' \"$*\" >>%q\nexec \"$@\"\n", logPath)
	if err := os.WriteFile(sudoStub, []byte(stubBody), 0o700); err != nil {
		t.Fatalf("writing the sudo stub: %v", err)
	}

	// A no-op stand-in for update-ca-certificates, on PATH so the script's
	// command -v check finds it.
	refreshStub := filepath.Join(bin, "update-ca-stand-in")
	if err := os.WriteFile(refreshStub, []byte("#!/bin/bash\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("writing the trust-store refresh stub: %v", err)
	}

	script := filepath.Join(mustFindModuleRoot(), "http-broker",
		"examples", "provision", "configure-http-broker.sh")

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HTTP_BROKER_CA_DEST="+caDest,
		"HTTP_BROKER_CA_BUNDLE="+seedBundle(t, home),
		"HTTP_BROKER_UPDATE_CA=update-ca-stand-in",
		"HTTP_BROKER_SUDO="+sudoStub,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the provisioning script: %v\n%s", err, out)
	}

	recorded, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the script never invoked the escalation prefix: %v", err)
	}
	got := string(recorded)

	if !strings.Contains(got, "install") || !strings.Contains(got, caDest) {
		t.Errorf("the CA install did not run through the escalation prefix:\n%s", got)
	}
	if !strings.Contains(got, "update-ca-stand-in") {
		t.Errorf("the trust-store refresh did not run through the escalation prefix:\n%s", got)
	}
}

func TestProvisionScriptFailsWithoutCopiedFiles(t *testing.T) {
	script := filepath.Join(mustFindModuleRoot(), "http-broker",
		"examples", "provision", "configure-http-broker.sh")

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("the script should fail when copy_paths has not shipped the files in")
	}
	if !strings.Contains(string(out), "copy_paths") {
		t.Errorf("the error should name copy_paths so the operator knows the fix:\n%s", out)
	}
}

// TestNeighbourToolsReachable is V-19 / AC-15: with the generated NO_PROXY
// applied and fallthrough "deny", stub listeners standing in for mcp-broker
// and local-gomod-proxy stay reachable.
//
// NOTE: this uses a Go client, so it proves the Go proxy semantics only. The
// real MCP client is the agent's own HTTP stack (Node's undici), which honours
// proxy variables inconsistently — the README records a manual check against
// the real agent wiring after first provisioning.
func TestNeighbourToolsReachable(t *testing.T) {
	// Stand-ins for the two neighbouring tools, both on loopback exactly as
	// they are in practice.
	mcpBroker := newUpstream(t)
	gomodProxy := newUpstream(t)

	s := startStack(t, stackOptions{Rules: rulesDoc("deny")})

	home := seedProvisionHome(t)
	caDest := filepath.Join(t.TempDir(), "ca-certificates", "http-broker.crt")
	bashrc := runProvision(t, home, caDest, seedBundle(t, home))

	// Parse NO_PROXY out of the generated block rather than restating it, so
	// this test fails if the script's carve-out changes.
	noProxy := extractExport(t, bashrc, "NO_PROXY")
	if noProxy == "" {
		t.Fatal("the generated block does not export NO_PROXY")
	}
	t.Logf("generated NO_PROXY: %s", noProxy)

	// A client configured exactly as the generated environment describes.
	client := proxiedClientHonouringNoProxy(t, s, noProxy)

	for name, stub := range map[string]*upstream{
		"mcp-broker stand-in":        mcpBroker,
		"local-gomod-proxy stand-in": gomodProxy,
	} {
		host, port := stub.HostPort(t)
		resp, body := requestThroughProxy(t, client, http.MethodGet,
			fmt.Sprintf("http://%s/ping", hostPort(host, port)))

		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 — NO_PROXY should keep it reachable under fallthrough deny",
				name, resp.StatusCode)
		}
		if body != "upstream ok" {
			t.Errorf("%s: body = %q, want the stub's response", name, body)
		}
		if stub.Count() == 0 {
			t.Errorf("%s: the stub was never reached, so the request went through the proxy", name)
		}
	}

	// The carve-out is targeted, not blanket: a host it does not name is still
	// routed through the proxy. Asserted against the proxy-selection function,
	// because every stand-in here is necessarily on loopback — which the
	// carve-out names — so a request-level assertion could not tell the two
	// apart.
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected an *http.Transport")
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/x", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	proxyFor, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("proxy selection: %v", err)
	}
	if proxyFor == nil {
		t.Error("a host outside NO_PROXY should be routed through the proxy")
	}
}

// proxiedClientHonouringNoProxy builds a client that routes through the proxy
// except for hosts NO_PROXY names.
func proxiedClientHonouringNoProxy(t *testing.T, s *stack, noProxy string) *http.Client {
	t.Helper()

	exempt := make(map[string]bool)
	for _, h := range strings.Split(noProxy, ",") {
		exempt[strings.TrimSpace(h)] = true
	}

	proxyURL := s.proxyURL()
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: s.track(&http.Transport{
			Proxy: func(r *http.Request) (*url.URL, error) {
				if exempt[r.URL.Hostname()] {
					return nil, nil
				}
				return proxyURL, nil
			},
		}),
	}
}

// extractExport pulls the value of an `export NAME="value"` line.
func extractExport(t *testing.T, script, name string) string {
	t.Helper()
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		prefix := "export " + name + "="
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return strings.Trim(strings.TrimPrefix(line, prefix), `"`)
	}
	return ""
}
