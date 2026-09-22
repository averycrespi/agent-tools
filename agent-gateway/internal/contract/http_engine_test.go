package contract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPProxyBoundsPreserveStreamingHeadroom(t *testing.T) {
	require.Equal(t, time.Hour, HTTPProxyTunnelLifetime)
	require.Equal(t, 32, HTTPProxyH2Streams)
	require.Equal(t, 96, HTTPProxyPrincipalWork)
	require.Equal(t, 128, HTTPProxyWork)
	require.Equal(t, 256, HTTPProxyConnections)
	require.Greater(t, HTTPProxyPrincipalWork, 32+32)
	require.Equal(t, 32*1024, HTTPProxyBufferBytes)
	require.Equal(t, 32*1024, HTTPProxyHeaderBytes)
	require.Equal(t, 10*time.Second, HTTPProxyDialTimeout)
	require.Equal(t, 15*time.Second, HTTPProxyHeaderTimeout)
	require.Equal(t, time.Minute, HTTPProxyIdleTimeout)
	require.Equal(t, 10*time.Second, HTTPProxyDrainTimeout)
	require.Equal(t, 4096, HTTPCAEnvelopeBytes)
	require.Equal(t, 256, HTTPCAEntries)
	require.Equal(t, 24*time.Hour, HTTPLeafLifetime)
}
