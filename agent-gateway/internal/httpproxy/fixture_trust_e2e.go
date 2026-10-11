//go:build e2e

package httpproxy

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

type fixtureTrust struct {
	roots         *x509.CertPool
	fixtureOrigin string
}

// Only the disposable binary can read this fixed file inside its isolated home.
// Trust is pinned to one numeric-loopback origin; no platform store is modified.
func (e *Engine) loadFixtureTrust() error {
	home := os.Getenv("AGENT_GATEWAY_E2E_ACCOUNT_HOME")
	if home == "" {
		return nil
	}
	path := filepath.Join(home, "demo-git-trust.json")
	invalid := errors.New("invalid owned demo Git trust material")
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || paths.ValidateOutputDirectory(home) != nil {
		return invalid
	}
	directory, err := os.Lstat(home) //nolint:gosec // Absolute selected E2E home passed owner/symlink validation above; this checks private mode.
	if err != nil || directory.Mode().Perm() != 0700 {
		return invalid
	}
	before, err := os.Lstat(path) //nolint:gosec // Fixed basename under the validated private E2E home; absence means normal platform trust.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || paths.ValidateOwnerOnlyFile(path) != nil {
		return invalid
	}
	f, err := os.Open(path) //nolint:gosec // Fixed basename passed private file validation; fstat below revalidates identity, owner, mode and link count.
	if err != nil {
		return invalid
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return invalid
	}
	stat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || int64(stat.Uid) != int64(os.Geteuid()) || after.Mode().Perm() != 0600 {
		return invalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return invalid
	}
	var input struct {
		Origin      string `json:"origin"`
		Certificate string `json:"certificate"`
	}
	if strictjson.Decode(raw, &input, strictjson.Options{MaxBytes: 8192, MaxDepth: 2, RejectUnknownMembers: true}) != nil {
		return invalid
	}
	address, err := netip.ParseAddrPort(strings.TrimPrefix(input.Origin, "https://"))
	if err != nil || input.Origin != "https://"+address.String() || !address.Addr().Is4() || !address.Addr().IsLoopback() || address.Port() == 0 {
		return invalid
	}
	block, rest := pem.Decode([]byte(input.Certificate))
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return invalid
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || cert.CheckSignatureFrom(cert) != nil || cert.VerifyHostname(address.Addr().String()) != nil {
		return invalid
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	e.roots, e.fixtureOrigin = roots, input.Origin
	return nil
}

func (e *Engine) upstreamRoots(target httppolicy.Request) *x509.CertPool {
	if e.fixtureOrigin != "" && target.URL().Scheme+"://"+target.URL().Host != e.fixtureOrigin {
		return nil
	}
	return e.roots
}
