//go:build e2e

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func writeHTTPProvisionCA(t *testing.T, home string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Disposable client fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".config", "agent-gateway", "http-ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
}

func TestHTTPProxyFreshProvisioningConvergesAndReadsRotatedToken(t *testing.T) {
	home, run := provisionFixture(t, "configure-agent-gateway-http-proxy.sh")
	writeProvisionToken(t, home, "agent-gateway", provisionToken)
	writeHTTPProvisionCA(t, home)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".bashrc"), []byte("export UNRELATED=preserved"), 0o600))
	require.True(t, run())
	first := readProvisionRC(t, home)
	require.True(t, strings.HasPrefix(first, "export UNRELATED=preserved\n# >>> agent-gateway-http-proxy >>>\n"))
	require.NotContains(t, first, provisionToken)
	require.True(t, run())
	require.Equal(t, first, readProvisionRC(t, home))
	runner, err := testutil.NewBinaryRunner(5*time.Second, 8192)
	require.NoError(t, err)
	for _, token := range []string{provisionToken, rotatedProvisionToken} {
		writeProvisionToken(t, home, "agent-gateway", token)
		t.Setenv("EXPECTED_TOKEN", token)
		result, err := runner.Run(t.Context(), "bash", "--noprofile", "--norc", "-x", "-c", `source "$HOME/.bashrc"; [[ "$HTTP_PROXY" == "http://agent:${EXPECTED_TOKEN}@host.lima.internal:8212" && "$HTTPS_PROXY" == "$HTTP_PROXY" && "$http_proxy" == "$HTTP_PROXY" && "$https_proxy" == "$HTTP_PROXY" && -z "$NO_PROXY" && -z "$no_proxy" && -r "$CURL_CA_BUNDLE" && "$REQUESTS_CA_BUNDLE" == "$SSL_CERT_FILE" && -r "$NODE_EXTRA_CA_CERTS" ]]`)
		require.NoError(t, err)
		require.NotContains(t, string(result.Stdout)+string(result.Stderr), token)
		require.Equal(t, first, readProvisionRC(t, home))
	}
	require.NoError(t, os.Chmod(filepath.Join(home, ".config", "agent-gateway", "agent-token"), 0o644))
	result, err := runner.Run(t.Context(), "bash", "--noprofile", "--norc", "-c", `source "$HOME/.bashrc"; [[ -z "${HTTP_PROXY+x}${HTTPS_PROXY+x}${http_proxy+x}${https_proxy+x}" ]]`)
	require.NoError(t, err)
	require.Contains(t, string(result.Stderr), "no proxy exports loaded")
}

func TestHTTPProxyAndMCPProvisioningRemainIndependent(t *testing.T) {
	home, runHTTP := provisionFixture(t, "configure-agent-gateway-http-proxy.sh")
	writeProvisionToken(t, home, "agent-gateway", provisionToken)
	writeHTTPProvisionCA(t, home)
	mcp, err := os.ReadFile("../../examples/provision/configure-agent-gateway.sh")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "standalone-mcp-provision")
	require.NoError(t, os.WriteFile(path, mcp, 0o700))
	runner, err := testutil.NewBinaryRunner(5*time.Second, 8192)
	require.NoError(t, err)
	var prior string
	for range 2 {
		result, err := runner.Run(t.Context(), "bash", path)
		require.NoError(t, err, "%s", result.Stderr)
		require.True(t, runHTTP())
		rc := readProvisionRC(t, home)
		require.Equal(t, 1, strings.Count(rc, "# >>> agent-gateway >>>"))
		require.Equal(t, 1, strings.Count(rc, "# >>> agent-gateway-http-proxy >>>"))
		require.NotContains(t, rc, provisionToken)
		if prior != "" {
			require.Equal(t, prior, rc)
		}
		prior = rc
	}
}

func TestHTTPProxyProvisioningRefusesConflictsAndUnsafeParents(t *testing.T) {
	for _, contents := range []string{"export HTTPS_PROXY=existing\n", "# >>> http-broker >>>\n# <<< http-broker <<<\n", "# >>> agent-gateway-http-proxy >>>\n", "# <<< agent-gateway-http-proxy <<<\n", "unrelated\x00bytes\n"} {
		t.Run(contents, func(t *testing.T) {
			home, run := provisionFixture(t, "configure-agent-gateway-http-proxy.sh")
			writeProvisionToken(t, home, "agent-gateway", provisionToken)
			writeHTTPProvisionCA(t, home)
			require.NoError(t, os.WriteFile(filepath.Join(home, ".bashrc"), []byte(contents), 0o600))
			require.False(t, run())
			require.Equal(t, contents, readProvisionRC(t, home))
		})
	}
	t.Run("symlink parent", func(t *testing.T) {
		home, run := provisionFixture(t, "configure-agent-gateway-http-proxy.sh")
		writeProvisionToken(t, home, "agent-gateway", provisionToken)
		writeHTTPProvisionCA(t, home)
		config := filepath.Join(home, ".config")
		require.NoError(t, os.Rename(config, config+"-real"))
		require.NoError(t, os.Symlink(config+"-real", config))
		require.False(t, run())
		_, err := os.Lstat(filepath.Join(config+"-real", "agent-gateway", "http-client-ca.pem"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}
