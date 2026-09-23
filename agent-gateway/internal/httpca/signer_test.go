package httpca

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type testClock struct{ at time.Time }

func (c *testClock) Now() time.Time { return c.at }

func TestSignerBindingsExpiryAndBounds(t *testing.T) {
	c := &testClock{time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)}
	payload, public, err := generate("01ARZ3NDEKTSV4RRFFQ69G5FAV", c, rand.Reader)
	require.NoError(t, err)
	defer clear(payload)
	require.LessOrEqual(t, len(payload), contract.HTTPCAEnvelopeBytes)
	signer, err := decode(payload, public, "01ARZ3NDEKTSV4RRFFQ69G5FAV", c, rand.Reader)
	require.NoError(t, err)
	defer signer.Close()
	_, err = decode(payload, public, "01ARZ3NDEKTSV4RRFFQ69G5FAW", c, rand.Reader)
	require.Error(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(signer.root)
	for _, host := range []string{"example.com", "127.0.0.1", "::1"} {
		cert, err := signer.Certificate(host)
		require.NoError(t, err)
		_, err = cert.Leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, CurrentTime: c.Now()})
		require.NoError(t, err)
	}
	for _, host := range []string{"EXAMPLE.com", "example.com.", "0177.0.0.1", "bad/host"} {
		_, err := signer.Certificate(host)
		require.Error(t, err)
	}
	for i := 0; i < contract.HTTPCAEntries+1; i++ {
		_, err := signer.Certificate(fmt.Sprintf("host%d.example.com", i))
		require.NoError(t, err)
		require.LessOrEqual(t, len(signer.cache), contract.HTTPCAEntries)
	}
	c.at = signer.root.NotAfter.Add(-time.Minute)
	cert, err := signer.Certificate("end.example.com")
	require.NoError(t, err)
	require.Equal(t, signer.root.NotAfter, cert.Leaf.NotAfter)
	c.at = signer.root.NotAfter
	_, err = signer.Certificate("end.example.com")
	require.Error(t, err)
	_, err = decode(payload, public, "01ARZ3NDEKTSV4RRFFQ69G5FAV", c, rand.Reader)
	require.Error(t, err)
}

func TestSignerRejectsMismatchedMaterialAndSaturation(t *testing.T) {
	c := &testClock{time.Now().UTC()}
	payload, public, err := generate("01ARZ3NDEKTSV4RRFFQ69G5FAV", c, rand.Reader)
	require.NoError(t, err)
	defer clear(payload)
	second, _, err := generate("01ARZ3NDEKTSV4RRFFQ69G5FAV", c, rand.Reader)
	require.NoError(t, err)
	defer clear(second)
	var a, b envelope
	require.NoError(t, json.Unmarshal(payload, &a))
	require.NoError(t, json.Unmarshal(second, &b))
	a.Key = b.Key
	wrong, err := json.Marshal(a)
	require.NoError(t, err)
	defer clear(wrong)
	_, err = decode(wrong, public, a.Installation, c, rand.Reader)
	require.Error(t, err)
	signer, err := decode(payload, public, a.Installation, c, rand.Reader)
	require.NoError(t, err)
	signer.mu.Lock()
	_, err = signer.Certificate("example.com")
	signer.mu.Unlock()
	require.Error(t, err)
	signer.Close()
	_, err = signer.Certificate("example.com")
	require.Error(t, err)
}
