package ca_test

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/egress-broker/internal/ca"
)

func newAuthority(t *testing.T) (*ca.Authority, string, string) {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca.key")
	certPath := filepath.Join(dir, "ca.pem")

	a, err := ca.LoadOrGenerate(keyPath, certPath)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
	return a, keyPath, certPath
}

func TestGeneratesOnceAndReloads(t *testing.T) {
	a, keyPath, certPath := newAuthority(t)
	first := string(a.RootPEM())

	for _, p := range []string{keyPath, certPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s should exist after generation: %v", p, err)
		}
	}

	// A second LoadOrGenerate against the same paths must load, not regenerate:
	// regenerating would silently invalidate every provisioned sandbox.
	b, err := ca.LoadOrGenerate(keyPath, certPath)
	if err != nil {
		t.Fatalf("second LoadOrGenerate: %v", err)
	}
	if string(b.RootPEM()) != first {
		t.Error("LoadOrGenerate regenerated an existing CA instead of loading it")
	}
}

func TestFileModes(t *testing.T) {
	_, keyPath, certPath := newAuthority(t)

	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := keyInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("ca.key mode = %v, want 0600: the private key must not be group or world readable", perm)
	}

	certInfo, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("stat cert: %v", err)
	}
	if perm := certInfo.Mode().Perm(); perm != 0o644 {
		t.Errorf("ca.pem mode = %v, want 0644", perm)
	}
}

func TestRootCannotSignIntermediates(t *testing.T) {
	a, _, _ := newAuthority(t)
	root := a.RootCert()

	if !root.IsCA {
		t.Fatal("root should be a CA")
	}
	if !root.BasicConstraintsValid {
		t.Error("BasicConstraintsValid should be set")
	}
	if root.MaxPathLen != 0 || !root.MaxPathLenZero {
		t.Errorf("MaxPathLen = %d, MaxPathLenZero = %v; want 0 and true so nothing this root signs can act as an intermediate CA",
			root.MaxPathLen, root.MaxPathLenZero)
	}
	if root.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("root should carry KeyUsageCertSign")
	}
}

func TestLeafBindsToRequestedHost(t *testing.T) {
	a, _, _ := newAuthority(t)

	cfg, err := a.ServerConfig("api.github.com")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	leaf := cfg.Certificates[0].Leaf

	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "api.github.com" {
		t.Errorf("DNSNames = %v, want [api.github.com]", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) != 0 {
		t.Errorf("IPAddresses = %v, want empty for a DNS target", leaf.IPAddresses)
	}
	if leaf.Subject.CommonName != "api.github.com" {
		t.Errorf("CommonName = %q, want api.github.com", leaf.Subject.CommonName)
	}
}

// TestLeafForIPLiteral is D11. A certificate carrying an IP in DNSNames fails
// verification with no useful message, so MITM of an allowed IP literal would
// break opaquely.
func TestLeafForIPLiteral(t *testing.T) {
	a, _, _ := newAuthority(t)

	cases := []struct {
		host string
		want string
	}{
		{"93.184.216.34", "93.184.216.34"},
		{"2606:2800:220:1:248:1893:25c8:1946", "2606:2800:220:1:248:1893:25c8:1946"},
	}

	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			cfg, err := a.ServerConfig(tc.host)
			if err != nil {
				t.Fatalf("ServerConfig: %v", err)
			}
			leaf := cfg.Certificates[0].Leaf

			if len(leaf.IPAddresses) != 1 {
				t.Fatalf("IPAddresses = %v, want exactly one IP SAN", leaf.IPAddresses)
			}
			if got := leaf.IPAddresses[0].String(); got != tc.want {
				t.Errorf("IP SAN = %s, want %s", got, tc.want)
			}
			if len(leaf.DNSNames) != 0 {
				t.Errorf("DNSNames = %v, want empty for an IP-literal target", leaf.DNSNames)
			}
		})
	}
}

// TestLeafVerifiesAgainstRoot proves an issued leaf actually chains, rather
// than merely carrying the fields we expect.
func TestLeafVerifiesAgainstRoot(t *testing.T) {
	a, _, _ := newAuthority(t)

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(a.RootPEM()) {
		t.Fatal("RootPEM should be parseable into a cert pool")
	}

	for _, host := range []string{"api.github.com", "93.184.216.34"} {
		cfg, err := a.ServerConfig(host)
		if err != nil {
			t.Fatalf("ServerConfig(%q): %v", host, err)
		}
		leaf := cfg.Certificates[0].Leaf

		// DNSName drives both cases: crypto/x509 checks it against IP SANs
		// when the string parses as an IP, and against DNS SANs otherwise.
		opts := x509.VerifyOptions{
			Roots:     roots,
			DNSName:   host,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		if _, err := leaf.Verify(opts); err != nil {
			t.Errorf("leaf for %q does not verify against the root: %v", host, err)
		}
	}
}

func TestLeafTLSSettings(t *testing.T) {
	a, _, _ := newAuthority(t)
	cfg, err := a.ServerConfig("api.github.com")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}

	if cfg.MinVersion != 0x0304 { // tls.VersionTLS13
		t.Errorf("MinVersion = %#x, want TLS 1.3", cfg.MinVersion)
	}
	want := []string{"h2", "http/1.1"}
	if len(cfg.NextProtos) != 2 || cfg.NextProtos[0] != want[0] || cfg.NextProtos[1] != want[1] {
		t.Errorf("NextProtos = %v, want %v", cfg.NextProtos, want)
	}
}

// TestLeafCacheReturnsSameConfig proves caching works, so a hot host does not
// pay key generation on every connection.
func TestLeafCacheReturnsSameConfig(t *testing.T) {
	a, _, _ := newAuthority(t)

	first, err := a.ServerConfig("api.github.com")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	second, err := a.ServerConfig("api.github.com")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if first != second {
		t.Error("a cached host should return the identical *tls.Config pointer")
	}
}

// TestLeafCacheKeyIsNormalised: two spellings of one host must share a
// certificate, or the cache both wastes entries and can be inflated by an
// agent varying case.
func TestLeafCacheKeyIsNormalised(t *testing.T) {
	a, _, _ := newAuthority(t)

	lower, err := a.ServerConfig("api.github.com")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	upper, err := a.ServerConfig("API.GitHub.COM.")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if lower != upper {
		t.Error("differently spelled forms of one host should share a cached certificate")
	}
}

// TestLeafCacheEvictsAtBound is the denial-of-service bound: an agent that
// CONNECTs to unique hosts must not grow the cache without limit.
func TestLeafCacheEvictsAtBound(t *testing.T) {
	if testing.Short() {
		t.Skip("issues LRUCap+ certificates")
	}
	a, _, _ := newAuthority(t)

	for i := range ca.LRUCap + 100 {
		if _, err := a.ServerConfig(fmt.Sprintf("host%d.example.com", i)); err != nil {
			t.Fatalf("ServerConfig: %v", err)
		}
	}
	if got := a.CacheLen(); got > ca.LRUCap {
		t.Errorf("cache holds %d entries, want at most the %d bound", got, ca.LRUCap)
	}
}

// TestClockSkewReissue is the skew buffer: a certificate close enough to
// expiry that a fast client would already reject it must be reissued rather
// than served.
func TestClockSkewReissue(t *testing.T) {
	a, _, _ := newAuthority(t)

	// Lifetime shorter than the skew buffer, so every issued cert is
	// immediately inside the window on the NotAfter side.
	a.SetLeafLifetime(time.Minute)
	a.SetSkewBuffer(5 * time.Minute)

	first, err := a.ServerConfig("api.github.com")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	second, err := a.ServerConfig("api.github.com")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if first == second {
		t.Error("a certificate expiring within the skew buffer should be reissued, not served from cache")
	}
}

func TestNoReissueOutsideSkewWindow(t *testing.T) {
	a, _, _ := newAuthority(t)
	a.SetLeafLifetime(24 * time.Hour)
	a.SetSkewBuffer(5 * time.Minute)

	first, err := a.ServerConfig("api.github.com")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	second, err := a.ServerConfig("api.github.com")
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if first != second {
		t.Error("a certificate well within its validity should be served from cache")
	}
}

func TestSweepDropsExpiringEntries(t *testing.T) {
	a, _, _ := newAuthority(t)
	a.SetLeafLifetime(time.Minute)
	a.SetSkewBuffer(0)

	if _, err := a.ServerConfig("api.github.com"); err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if a.CacheLen() != 1 {
		t.Fatalf("cache length = %d, want 1", a.CacheLen())
	}

	// Sweep looking an hour ahead: a one-minute certificate is inside it.
	a.SetSweepBuffer(time.Hour)
	a.SweepExpired()

	if got := a.CacheLen(); got != 0 {
		t.Errorf("cache length after sweep = %d, want 0", got)
	}
}

func TestRotateReplacesRootAndClearsCache(t *testing.T) {
	a, keyPath, certPath := newAuthority(t)

	before := string(a.RootPEM())
	if _, err := a.ServerConfig("api.github.com"); err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	if a.CacheLen() == 0 {
		t.Fatal("expected a cached leaf before rotation")
	}

	if err := a.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	if string(a.RootPEM()) == before {
		t.Error("Rotate should install a different root")
	}
	if got := a.CacheLen(); got != 0 {
		t.Errorf("cache length after rotate = %d, want 0: leaves signed by the old root must not be served", got)
	}

	// The new root must be what is on disk, so a sibling process loading the
	// files agrees with the running one.
	onDisk, err := ca.LoadOrGenerate(keyPath, certPath)
	if err != nil {
		t.Fatalf("LoadOrGenerate after rotate: %v", err)
	}
	if string(onDisk.RootPEM()) != string(a.RootPEM()) {
		t.Error("the rotated root on disk differs from the one in memory")
	}
}

func TestReloadPicksUpExternalRotation(t *testing.T) {
	a, keyPath, certPath := newAuthority(t)
	before := string(a.RootPEM())

	// A separate process runs `ca rotate`.
	sibling, err := ca.LoadOrGenerate(keyPath, certPath)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
	if err := sibling.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	if string(a.RootPEM()) != before {
		t.Fatal("the running authority should not change until it reloads")
	}
	if err := a.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if string(a.RootPEM()) != string(sibling.RootPEM()) {
		t.Error("Reload should pick up the rotated root")
	}
}

// TestReloadKeepsPreviousOnError: a failed reload must leave the running CA
// serving, or SIGHUP against a broken file takes down every sandbox.
func TestReloadKeepsPreviousOnError(t *testing.T) {
	a, _, certPath := newAuthority(t)
	before := string(a.RootPEM())

	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o644); err != nil {
		t.Fatalf("corrupt cert: %v", err)
	}
	if err := a.Reload(); err == nil {
		t.Fatal("Reload on a corrupt certificate = nil, want an error")
	}
	if string(a.RootPEM()) != before {
		t.Error("a failed Reload must leave the previous root live")
	}
}

func TestLoadRejectsMismatchedPair(t *testing.T) {
	_, keyPath, certPath := newAuthority(t)

	// Swap in a certificate from a different CA.
	otherDir := t.TempDir()
	other, err := ca.LoadOrGenerate(filepath.Join(otherDir, "ca.key"), filepath.Join(otherDir, "ca.pem"))
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
	if err := os.WriteFile(certPath, other.RootPEM(), 0o644); err != nil {
		t.Fatalf("write mismatched cert: %v", err)
	}

	_, err = ca.LoadOrGenerate(keyPath, certPath)
	if err == nil {
		t.Fatal("a certificate that does not match the key should be rejected")
	}
	if !strings.Contains(err.Error(), "do not match") {
		t.Errorf("error %q should say the pair does not match", err)
	}
}

func TestLoadRejectsHalfPresentPair(t *testing.T) {
	_, keyPath, certPath := newAuthority(t)
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key: %v", err)
	}

	_, err := ca.LoadOrGenerate(keyPath, certPath)
	if err == nil {
		t.Fatal("a certificate with no key should be reported, not silently regenerated")
	}
	if !strings.Contains(err.Error(), "only one of") {
		t.Errorf("error %q should explain that only one file was found", err)
	}
}

func TestLoadRejectsGarbagePEM(t *testing.T) {
	_, keyPath, certPath := newAuthority(t)
	if err := os.WriteFile(keyPath, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ca.LoadOrGenerate(keyPath, certPath); err == nil {
		t.Error("a key file with no PEM block should be rejected")
	}
	_ = certPath
}

// TestRootPEMIsASingleCertificate guards what provisioning installs: extra
// blocks, or a key accidentally concatenated, would be shipped into sandboxes.
func TestRootPEMIsASingleCertificate(t *testing.T) {
	a, _, _ := newAuthority(t)

	block, rest := pem.Decode(a.RootPEM())
	if block == nil {
		t.Fatal("RootPEM should decode")
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("PEM block type = %q, want CERTIFICATE", block.Type)
	}
	if len(strings.TrimSpace(string(rest))) != 0 {
		t.Errorf("RootPEM should contain exactly one PEM block, found trailing data: %q", rest)
	}
}
