//go:build integration

package httpproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

// Exchange facts deliberately exclude bodies, headers and credentials. These
// native fixtures run under Git policy, not HTTP-only request permission.
type gitExchange struct {
	path, protocol string
	bytes          int
	chunked, flush bool
	status         int
}

type nativeGitFixture struct {
	t                                                 *testing.T
	proxy                                             *proxyFixture
	runner                                            *testutil.BinaryRunner
	git, env, root, upstream, client, wrapper, remote string
	secret                                            []byte
	mu                                                sync.Mutex
	exchanges                                         []gitExchange
	interrupt                                         bool
	dispatched                                        chan struct{}
	settled                                           chan struct{}
}

func newNativeGitFixture(t *testing.T) *nativeGitFixture {
	t.Helper()
	git, err := exec.LookPath("git")
	require.NoError(t, err, "native Git is required for transport qualification")
	env, err := exec.LookPath("env")
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(20*time.Second, 8<<20)
	require.NoError(t, err)
	n := &nativeGitFixture{t: t, proxy: fixture(t), runner: runner, git: git, env: env, root: t.TempDir(), dispatched: make(chan struct{}), settled: make(chan struct{})}
	n.upstream = filepath.Join(n.root, "upstream.git")
	n.client = filepath.Join(n.root, "client")
	require.NoError(t, os.Mkdir(filepath.Join(n.root, "home"), 0700))
	n.run(n.root, "init", "--bare", n.upstream)
	n.run(n.root, "--git-dir="+n.upstream, "config", "http.receivepack", "true")
	seed := filepath.Join(n.root, "seed")
	n.run(n.root, "init", "--initial-branch=main", seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "seed.txt"), []byte("native transport seed\n"), 0600))
	n.run(seed, "add", "seed.txt")
	n.run(seed, "commit", "-m", "Seed disposable upstream")
	n.run(seed, "push", n.upstream, "main")
	n.run(n.root, "--git-dir="+n.upstream, "symbolic-ref", "HEAD", "refs/heads/main")
	execPath := strings.TrimSpace(string(n.run(n.root, "--exec-path")))
	backend := filepath.Join(execPath, "git-http-backend")
	_, err = os.Stat(backend)
	require.NoError(t, err)
	n.secret = make([]byte, 32)
	_, err = rand.Read(n.secret)
	require.NoError(t, err)
	// Hex encoding makes an HTTP-safe private test credential without retaining it.
	secret := fmt.Sprintf("%x", n.secret)
	n.secret = []byte(secret)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ordinary" || strings.HasPrefix(r.URL.Path, "/upstream.git/releases/") || r.URL.Path == "/upstream.git/issues" {
			n.record(gitExchange{path: r.URL.Path, status: 200})
			_, _ = io.WriteString(w, "ordinary HTTP")
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("Proxy-Authorization") != "" {
			w.WriteHeader(403)
			return
		}
		body, readErr := io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
		if readErr != nil || len(body) > 8<<20 {
			w.WriteHeader(400)
			return
		}
		fact := gitExchange{path: r.URL.Path, protocol: r.Header.Get("Git-Protocol"), bytes: len(body), flush: bytes.Equal(body, []byte("0000")), chunked: len(r.TransferEncoding) > 0}
		n.mu.Lock()
		interrupted := n.interrupt && r.URL.Path == "/upstream.git/git-receive-pack" && !fact.flush
		n.mu.Unlock()
		if interrupted {
			n.record(fact)
			close(n.dispatched)
			// Deliberately withhold execution/completion after dispatch. Cancellation
			// does not establish rollback for a real upstream.
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			close(n.settled)
			return
		}
		args := append(n.cleanEnv(), "GIT_PROJECT_ROOT="+n.root, "GIT_HTTP_EXPORT_ALL=1", "REQUEST_METHOD="+r.Method, "PATH_INFO="+r.URL.Path, "QUERY_STRING="+r.URL.RawQuery, "CONTENT_TYPE="+r.Header.Get("Content-Type"), fmt.Sprintf("CONTENT_LENGTH=%d", len(body)), "REMOTE_USER=fixture", "HTTP_GIT_PROTOCOL="+r.Header.Get("Git-Protocol"), backend)
		p, input, startErr := n.runner.StartWithInputPipe(r.Context(), n.env, args...)
		if startErr != nil {
			t.Error("smart HTTP backend failed to start")
			w.WriteHeader(500)
			return
		}
		_, writeErr := input.Write(body)
		closeErr := input.Close()
		result, runErr := p.Wait()
		n.checkResult(result)
		if writeErr != nil || closeErr != nil || runErr != nil {
			t.Error("smart HTTP backend failed")
			w.WriteHeader(500)
			return
		}
		response, parseErr := http.ReadResponse(bufio.NewReader(bytes.NewReader(append([]byte("HTTP/1.1 200 OK\r\n"), result.Stdout...))), r)
		if parseErr != nil {
			t.Error("invalid smart HTTP CGI response")
			w.WriteHeader(500)
			return
		}
		defer func() { _ = response.Body.Close() }()
		status := 200
		if value := response.Header.Get("Status"); value != "" {
			_, _ = fmt.Sscanf(value, "%d", &status)
		}
		fact.status = status
		n.record(fact)
		for key, values := range response.Header {
			if key != "Status" {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
		}
		w.WriteHeader(status)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(server.Close)
	n.proxy.engine.roots = x509.NewCertPool()
	n.proxy.engine.roots.AddCert(server.Certificate())
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	addr, err := netip.ParseAddrPort(u.Host)
	require.NoError(t, err)
	require.True(t, addr.IsValid())
	material, err := n.proxy.gitMaterials.Create(audit.WithSystem(t.Context()), contract.GitCredentialDefinition{Name: "native-git-fixture", Origin: server.URL, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, append([]byte(nil), n.secret...))
	require.NoError(t, err)
	// This grant supplies only explicit private-network permission. It selects
	// no HTTP material; Git admission still requires its independent ref policy.
	n.proxy.allow(t, server.URL, "allow_requests", "/upstream.git", "")
	n.remote = server.URL + "/upstream.git"
	repository, err := n.proxy.authority.PutGitRepository(audit.WithSystem(t.Context()), "", "", contract.GitRepositoryDefinition{Name: "native", URL: n.remote, Aliases: []string{}, CredentialID: &material.ID})
	require.NoError(t, err)
	_, err = n.proxy.authority.PutGitGrant(audit.WithSystem(t.Context()), "", "", authorization.GitGrantInput{PrincipalID: n.proxy.credential.Principal.ID, RepositoryID: repository.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/"},"actions":["create","update","delete"]},{"ref":{"kind":"prefix","value":"refs/tags/"},"actions":["create","update","delete"]}]}`)})
	require.NoError(t, err)
	profile, err := n.proxy.authority.GetGitRoutingProfile(t.Context())
	require.NoError(t, err)
	_, err = n.proxy.authority.PutGitRoutingProfile(audit.WithSystem(t.Context()), profile.Revision, []string{server.URL})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(n.root, "ca.pem"), n.proxy.publicCA, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(n.root, "proxy-token"), []byte(n.proxy.credential.Bearer), 0600))
	n.wrapper = filepath.Join(n.root, "client-git.sh")
	// The supported proxy-secret environment sink is filled inside the child,
	// never argv or persisted Git configuration. No upstream secret is exported.
	script := "#!/bin/sh\nset -eu\nroot=$1\nproxy=$2\ngit=$3\nshift 3\nexport https_proxy=\"http://agent:$(cat \"$root/proxy-token\")@$proxy\"\nexport GIT_SSL_CAINFO=\"$root/ca.pem\"\nexec \"$git\" -c http.version=HTTP/1.1 -c credential.helper= \"$@\"\n"
	require.NoError(t, os.WriteFile(n.wrapper, []byte(script), 0700))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, n.proxy.engine.Close(ctx))
		server.Close()
		for _, address := range []string{n.proxy.address, server.Listener.Addr().String()} {
			conn, dialErr := net.DialTimeout("tcp", address, time.Second)
			if conn != nil {
				_ = conn.Close()
			}
			require.Error(t, dialErr, "owned native Git listener survived cleanup")
		}
		history, err := n.proxy.traffic.GitHistory(ctx, 0, 256)
		require.NoError(t, err)
		encoded, err := json.Marshal(history)
		require.NoError(t, err)
		for _, canary := range [][]byte{n.secret, []byte(n.proxy.credential.Bearer)} {
			scanner, err := testutil.NewCanaryScanner(canary)
			require.NoError(t, err)
			require.NoError(t, scanner.Scan("retained Git traffic", bytes.NewReader(encoded)))
		}
		require.NoError(t, os.RemoveAll(n.root))
		_, err = os.Stat(n.root)
		require.True(t, os.IsNotExist(err), "native Git temporary material survived cleanup")
	})
	return n
}

func (n *nativeGitFixture) cleanEnv() []string {
	return []string{"-i", "PATH=/usr/bin:/bin", "HOME=" + filepath.Join(n.root, "home"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "LC_ALL=C"}
}
func (n *nativeGitFixture) checkResult(r testutil.ProcessResult) {
	n.t.Helper()
	require.True(n.t, r.Cleanup.Reaped, "native child must be reaped")
	require.False(n.t, r.Cleanup.Survived, "native child group survived")
	require.False(n.t, r.StdoutTruncated)
	require.False(n.t, r.StderrTruncated)
	for _, canary := range [][]byte{n.secret, []byte(n.proxy.credential.Bearer)} {
		if len(canary) == 0 {
			continue
		}
		scanner, err := testutil.NewCanaryScanner(canary)
		require.NoError(n.t, err)
		require.NoError(n.t, scanner.Scan("Git stdout", bytes.NewReader(r.Stdout)))
		require.NoError(n.t, scanner.Scan("Git stderr", bytes.NewReader(r.Stderr)))
	}
}
func (n *nativeGitFixture) run(dir string, args ...string) []byte {
	n.t.Helper()
	result, err := n.runner.RunInDir(n.t.Context(), dir, n.env, append(n.cleanEnv(), append([]string{n.git}, args...)...)...)
	n.checkResult(result)
	require.NoError(n.t, err, "disposable Git command failed (output withheld)")
	return result.Stdout
}
func (n *nativeGitFixture) clientArgs(args ...string) []string {
	return append(n.cleanEnv(), append([]string{"/bin/sh", n.wrapper, n.root, n.proxy.address, n.git}, args...)...)
}
func (n *nativeGitFixture) clientRun(dir string, args ...string) testutil.ProcessResult {
	n.t.Helper()
	result, err := n.runner.RunInDir(n.t.Context(), dir, n.env, n.clientArgs(args...)...)
	n.checkResult(result)
	require.NoError(n.t, err, "native proxied Git command failed (output withheld)")
	return result
}
func (n *nativeGitFixture) record(f gitExchange) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.exchanges = append(n.exchanges, f)
}
func (n *nativeGitFixture) facts() []gitExchange {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]gitExchange(nil), n.exchanges...)
}
func (n *nativeGitFixture) ref(name string) string {
	return strings.TrimSpace(string(n.run(n.root, "--git-dir="+n.upstream, "rev-parse", name)))
}
func (n *nativeGitFixture) commit(name string) string {
	require.NoError(n.t, os.WriteFile(filepath.Join(n.client, "change.txt"), []byte(name), 0600))
	n.run(n.client, "add", "change.txt")
	n.run(n.client, "commit", "-m", name)
	return strings.TrimSpace(string(n.run(n.client, "rev-parse", "HEAD")))
}

func TestIntegrationNativeGitHTTPSRefsAndStreaming(t *testing.T) {
	n := newNativeGitFixture(t)
	n.clientRun(n.root, "-c", "protocol.version=2", "clone", n.remote, n.client)
	require.Equal(t, n.ref("refs/heads/main"), strings.TrimSpace(string(n.run(n.client, "rev-parse", "HEAD"))))
	require.Equal(t, "native transport seed\n", string(mustReadGitFile(t, filepath.Join(n.client, "seed.txt"))))
	for _, version := range []string{"2", "0"} {
		seed := filepath.Join(n.root, "seed")
		content := "upstream fetch protocol " + version + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(seed, "seed.txt"), []byte(content), 0600))
		n.run(seed, "add", "seed.txt")
		n.run(seed, "commit", "-m", "Advance upstream for native fetch")
		n.run(seed, "push", n.upstream, "main")
		n.clientRun(n.client, "-c", "protocol.version="+version, "fetch", "origin")
		require.Equal(t, n.ref("refs/heads/main"), strings.TrimSpace(string(n.run(n.client, "rev-parse", "refs/remotes/origin/main"))))
		require.Equal(t, content, string(n.run(n.client, "show", "refs/remotes/origin/main:seed.txt")))
		result := n.clientRun(n.client, "-c", "protocol.version="+version, "ls-remote", "origin", "refs/heads/main")
		require.Contains(t, string(result.Stdout), n.ref("refs/heads/main")+"\trefs/heads/main")
	}
	n.run(n.client, "checkout", "-b", "topic")
	first := n.commit("First branch update")
	n.clientRun(n.client, "push", "origin", "topic")
	require.Equal(t, first, n.ref("refs/heads/topic"))
	second := n.commit("Second branch update")
	n.run(n.client, "tag", "fixture-tag")
	n.clientRun(n.client, "push", "--atomic", "origin", "topic", "refs/tags/fixture-tag", "HEAD:refs/heads/other")
	for _, ref := range []string{"refs/heads/topic", "refs/heads/other", "refs/tags/fixture-tag"} {
		require.Equal(t, second, n.ref(ref))
	}
	n.clientRun(n.client, "push", "origin", ":refs/heads/other")
	result, err := n.runner.RunInDir(t.Context(), n.root, n.env, append(n.cleanEnv(), n.git, "--git-dir="+n.upstream, "show-ref", "--verify", "refs/heads/other")...)
	n.checkResult(result)
	require.Error(t, err)
	require.NotZero(t, result.ExitCode)
	// Native libcurl emits a separate flush-only discovery probe before streaming
	// a pack beyond http.postBuffer. Incompressible data forces actual pack size.
	payload := make([]byte, 2<<20)
	_, err = rand.Read(payload)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(n.client, "large.bin"), payload, 0600))
	n.run(n.client, "add", "large.bin")
	n.run(n.client, "commit", "-m", "Large disposable pack")
	large := strings.TrimSpace(string(n.run(n.client, "rev-parse", "HEAD")))
	n.clientRun(n.client, "-c", "http.postBuffer=65536", "push", "origin", "topic")
	require.Equal(t, large, n.ref("refs/heads/topic"))
	// Native receive-pack rejects a ref even though its smart HTTP response is 200.
	n.run(n.root, "--git-dir="+n.upstream, "config", "receive.denyDeletes", "true")
	before := len(n.facts())
	result, err = n.runner.RunInDir(t.Context(), n.client, n.env, n.clientArgs("push", "origin", ":refs/heads/topic")...)
	n.checkResult(result)
	require.Error(t, err)
	require.NotZero(t, result.ExitCode)
	require.Equal(t, large, n.ref("refs/heads/topic"))
	rejection200 := false
	for _, f := range n.facts()[before:] {
		if f.path == "/upstream.git/git-receive-pack" && f.status == 200 {
			rejection200 = true
		}
	}
	require.True(t, rejection200, "Git rejection must be observed independently of HTTP status")
	v2, fallback, probe, stream := false, false, false, false
	for _, f := range n.facts() {
		if strings.HasSuffix(f.path, "git-upload-pack") && f.protocol == "version=2" {
			v2 = true
		}
		if f.path == "/upstream.git/info/refs" && f.protocol == "" {
			fallback = true
		}
		if strings.HasSuffix(f.path, "git-receive-pack") {
			if f.flush && f.bytes == 4 {
				probe = true
			}
			if f.bytes > 1<<20 && f.chunked {
				require.True(t, probe, "flush probe precedes large streaming push")
				stream = true
			}
		}
	}
	require.True(t, v2, "native fetch protocol v2 was not forwarded")
	require.True(t, fallback, "native protocol fallback was not exercised")
	require.True(t, stream, "native large chunked pack was not forwarded")
	require.Equal(t, large, strings.TrimSpace(string(n.run(n.root, "--git-dir="+n.upstream, "log", "-1", "--format=%H", "topic"))))
}

func TestIntegrationNativeGitInterruptedPushNoReplay(t *testing.T) {
	n := newNativeGitFixture(t)
	n.clientRun(n.root, "clone", n.remote, n.client)
	initial := n.ref("refs/heads/main")
	n.commit("Interrupted disposable push")
	beforePush, err := n.proxy.traffic.GitHistory(t.Context(), 0, 256)
	require.NoError(t, err)
	n.mu.Lock()
	n.interrupt = true
	n.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p, err := n.runner.Start(ctx, n.env, n.clientArgs("-C", n.client, "push", "origin", "HEAD:refs/heads/main")...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Stop()) })
	select {
	case <-n.dispatched:
	case <-time.After(10 * time.Second):
		t.Fatal("push did not dispatch")
	}
	cancel()
	result, err := p.Wait()
	n.checkResult(result)
	require.Error(t, err)
	select {
	case <-n.settled:
	case <-time.After(6 * time.Second):
		t.Fatal("interrupted upstream handler failed to settle")
	}
	require.Eventually(t, func() bool { return n.proxy.engine.Status().Work.InUse == 0 }, 5*time.Second, 10*time.Millisecond)
	count := 0
	for _, f := range n.facts() {
		if f.path == "/upstream.git/git-receive-pack" && !f.flush {
			count++
		}
	}
	require.Equal(t, 1, count, "Gateway must not replay dispatched receive-pack")
	history, err := n.proxy.traffic.GitHistory(t.Context(), beforePush.HighWater, 256)
	require.NoError(t, err)
	interruptedRecords := 0
	for _, record := range history.Records {
		if record.Admission.Operation == "push" {
			interruptedRecords++
			// Even a recorded terminal transport observation is not a Git
			// completion receipt. Absent terminal evidence is unknown as well.
			if record.Completion != nil {
				require.Equal(t, "outcome_unknown", record.Completion.Outcome)
			}
		}
	}
	require.Equal(t, 1, interruptedRecords)
	require.Equal(t, initial, n.ref("refs/heads/main"), "fixture deliberately withheld upstream execution, not a general rollback guarantee")
}

func TestIntegrationNativeGitDeniedCommandsNeverDispatch(t *testing.T) {
	n := newNativeGitFixture(t)
	n.clientRun(n.root, "clone", n.remote, n.client)
	grants, err := n.proxy.authority.ListGitGrants(t.Context())
	require.NoError(t, err)
	require.Len(t, grants, 1)
	grant := grants[0]
	_, err = n.proxy.authority.PutGitGrant(audit.WithSystem(t.Context()), grant.ID, grant.Revision, authorization.GitGrantInput{PrincipalID: grant.PrincipalID, RepositoryID: grant.RepositoryID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[{"ref":{"kind":"exact","value":"refs/heads/allowed"},"actions":["create"]}]}`)})
	require.NoError(t, err)
	zero, one := strings.Repeat("0", 40), strings.Repeat("1", 40)
	packet := func(ref string) string {
		value := zero + " " + one + " " + ref
		return fmt.Sprintf("%04x%s", len(value)+4, value)
	}
	for _, test := range []struct{ path, method, body, contentType string }{
		{"/upstream.git/git-receive-pack", "POST", packet("refs/heads/allowed") + packet("refs/heads/denied") + "0000PACKopaque", "application/x-git-receive-pack-request"},
		{"/upstream.git/git-receive-pack", "POST", "0000trailing", "application/x-git-receive-pack-request"},
		{"/unknown.git/git-receive-pack", "POST", "0000", "application/x-git-receive-pack-request"},
		{"/upstream.git/git-receive-pack", "POST", "0000", "text/plain"},
		{"/upstream.git/info/refs?service=git-upload-pack&service=git-receive-pack", "GET", "", ""},
		{"/upstream.git/git%252dreceive-pack", "POST", "0000", "application/x-git-receive-pack-request"},
	} {
		t.Run(test.path+test.contentType, func(t *testing.T) {
			before := len(n.facts())
			prior, historyErr := n.proxy.traffic.GitHistory(t.Context(), 0, 256)
			require.NoError(t, historyErr)
			conn := n.proxy.intercept(t, n.remote, "http/1.1")
			u, err := url.Parse(n.remote)
			require.NoError(t, err)
			_, err = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", test.method, test.path, u.Host, test.contentType, len(test.body), test.body)
			require.NoError(t, err)
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: test.method})
			require.NoError(t, err)
			require.GreaterOrEqual(t, response.StatusCode, 400)
			_, err = io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.NoError(t, conn.Close())
			require.Equal(t, before, len(n.facts()), "denial must make zero upstream exchanges")
			require.Eventually(t, func() bool {
				history, err := n.proxy.traffic.GitHistory(t.Context(), 0, 256)
				return err == nil && len(history.Records) == len(prior.Records)+1
			}, 3*time.Second, 10*time.Millisecond)
			history, historyErr := n.proxy.traffic.GitHistory(t.Context(), 0, 256)
			require.NoError(t, historyErr)
			require.Len(t, history.Records, len(prior.Records)+1, "one Git admission per classified rejection")
			require.False(t, history.Records[len(history.Records)-1].Admission.Allowed)
		})
	}
	// Profile activation does not take ordinary pages away from HTTP policy.
	u, err := url.Parse(n.remote)
	require.NoError(t, err)
	n.proxy.allow(t, u.Scheme+"://"+u.Host, "allow_requests", "", "")
	for _, ordinary := range []string{"/ordinary", "/ordinary?q=service", "/ordinary?q=git-upload-pack&service=search", "/upstream.git/issues?q=service", "/upstream.git/releases/download/v1/HEAD", "/upstream.git/releases/download/objects/app.zip", "/upstream.git/releases/download/v1/git-receive-pack.exe", "/upstream.git/releases/download/v1/git-receive-pack"} {
		conn := n.proxy.intercept(t, n.remote, "http/1.1")
		_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", ordinary, u.Host)
		require.NoError(t, err)
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
		require.NoError(t, err)
		require.Equal(t, 200, response.StatusCode, ordinary)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, "ordinary HTTP", string(body))
		require.NoError(t, response.Body.Close())
		require.NoError(t, conn.Close())
	}
}

func mustReadGitFile(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	require.NoError(t, err)
	return value
}
