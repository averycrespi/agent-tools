//go:build e2e

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

func startDemoFixture(ctx context.Context, kind, root string, env []string, opts options, children *[]*child, endpoints map[string]string) error {
	if ctx.Err() != nil {
		return errors.New("interrupted before " + kind + " fixture")
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("demo executable lookup failed")
	}
	endpoint := filepath.Join(root, kind+"-port")
	argv := append([]string{executable}, opts.fixturePrefix...)
	argv = append(argv, "--fixture", kind, endpoint)
	fixture, err := opts.start(kind+" fixture", argv, env)
	if err != nil {
		return err
	}
	*children = append(*children, fixture)
	return waitUntil(ctx, *children, "fixture startup", func() (bool, error) {
		raw, err := os.ReadFile(endpoint)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, errors.New("fixture endpoint read failed")
		}
		port, err := strconv.Atoi(string(raw))
		if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != string(raw) {
			// Exclusive publication may be observed between create and write.
			return false, nil
		}
		endpoints[kind] = string(raw)
		return true, nil
	})
}

func gitEnvironment(home string) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Demo", "GIT_AUTHOR_EMAIL=demo@example.invalid", "GIT_COMMITTER_NAME=Demo", "GIT_COMMITTER_EMAIL=demo@example.invalid", "LC_ALL=C"}
}

func runGit(ctx context.Context, home string, children *[]*child, args ...string) ([]byte, int, error) {
	if ctx.Err() != nil {
		return nil, -1, errors.New("interrupted before native Git")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, -1, errors.New("native Git is required for the curated demo")
	}
	command, err := startChild("native Git", append([]string{git}, args...), gitEnvironment(home))
	if err != nil {
		return nil, -1, err
	}
	*children = append(*children, command)
	if err = command.finish(ctx, 10*time.Second, false); err != nil {
		return nil, -1, err
	}
	*children = (*children)[:len(*children)-1]
	if command.code < 0 {
		return nil, command.code, errors.New("native Git timed out or was interrupted")
	}
	return command.stdout.bytes(), command.code, nil
}

func demoGitCertificate() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Disposable demo Git fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// The supervisor owns every native process before publishing the fixture.
// Its immutable repository needs only one native upload-pack advertisement.
func prepareGitRepository(ctx context.Context, root string, children *[]*child) error {
	home := filepath.Join(root, "home")
	repository := filepath.Join(root, "demo-notes.git")
	seed := filepath.Join(root, "git-seed")
	for _, args := range [][]string{{"init", "--bare", repository}, {"init", "--initial-branch=main", seed}} {
		_, code, err := runGit(ctx, home, children, args...)
		if err != nil || code != 0 {
			return errors.New("demo Git repository initialization failed")
		}
	}
	if err := writePrivate(filepath.Join(seed, "README.md"), []byte("# Demo Notes\nOwned local Git discovery fixture. No external service is contacted.\n")); err != nil {
		return err
	}
	for _, args := range [][]string{{"-C", seed, "add", "README.md"}, {"-C", seed, "commit", "-m", "Seed disposable demo notes"}, {"-C", seed, "push", repository, "main"}, {"--git-dir=" + repository, "symbolic-ref", "HEAD", "refs/heads/main"}} {
		_, code, err := runGit(ctx, home, children, args...)
		if err != nil || code != 0 {
			return errors.New("demo Git repository seed failed")
		}
	}
	advertisement, code, err := runGit(ctx, home, children, "upload-pack", "--stateless-rpc", "--advertise-refs", repository)
	if err != nil || code != 0 {
		return errors.New("native Git advertisement failed")
	}
	return writePrivate(filepath.Join(root, "git-advertisement"), advertisement)
}

// The fixture exposes immutable native discovery, not arbitrary paths or writes.
// No request starts a process; supervisor cancellation cannot orphan descendants.
func serveGitFixture(ctx context.Context, endpoint string) error {
	root := filepath.Dir(endpoint)
	home := filepath.Join(root, "home")
	file, err := os.Open(filepath.Join(root, "git-advertisement")) //nolint:gosec // Fixed nonsecret native output under the supervisor's fresh disposable root, never a request path.
	if err != nil {
		return errors.New("demo Git advertisement unavailable")
	}
	advertisement, readErr := io.ReadAll(io.LimitReader(file, outputLimit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(advertisement) > outputLimit {
		return errors.New("demo Git advertisement unavailable")
	}
	body := append([]byte("001e# service=git-upload-pack\n0000"), advertisement...)
	var secretBytes [32]byte
	if _, err := rand.Read(secretBytes[:]); err != nil {
		return err
	}
	secret := hex.EncodeToString(secretBytes[:])
	if err := writePrivate(filepath.Join(root, "git-upstream-secret"), []byte(secret)); err != nil {
		return err
	}
	certificate, public, err := demoGitCertificate()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return err
	}
	trust, err := json.Marshal(object{"origin": "https://" + listener.Addr().String(), "certificate": string(public)})
	if err != nil {
		return err
	}
	if err = writePrivate(filepath.Join(home, "demo-git-trust.json"), trust); err != nil {
		return err
	}
	var allowed, rejected atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.RequestURI() == "/counts" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(object{"allowed": allowed.Load(), "rejected": rejected.Load()})
			return
		}
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/demo-notes.git/info/refs?service=git-upload-pack" || r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Git-Protocol") != "" {
			rejected.Add(1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		allowed.Add(1)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body) //nolint:gosec // Native Git packet lines served as application/x-git-upload-pack-advertisement, never HTML or request-derived content.
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}))
	}()
	if err = writePrivate(endpoint, []byte(port)); err != nil {
		_ = server.Close()
		<-done
		return err
	}
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		<-done
		return err
	case err = <-done:
		return err
	}
}
