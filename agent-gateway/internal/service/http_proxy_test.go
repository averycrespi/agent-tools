package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPProxyInstalledSelectionRoundTrip(t *testing.T) {
	base := Settings{Binary: "/opt/bin/agent-gateway", DataDir: "/private/gateway", Listen: "127.0.0.1:8210"}
	require.NoError(t, base.validate())
	parsed, err := parseArguments(base.arguments())
	require.NoError(t, err)
	require.Empty(t, parsed.HTTPProxyListen)
	base.HTTPProxyListen = "127.0.0.1:8212"
	parsed, err = parseArguments(base.arguments())
	require.NoError(t, err)
	require.Equal(t, base, parsed)
	for _, authority := range []string{"127.0.0.1:8210", "0.0.0.0:8212", "localhost:8212", "127.0.0.1:0", "127.0.0.1:08212"} {
		changed := base
		changed.HTTPProxyListen = authority
		require.Error(t, changed.validate())
	}
	_, err = parseArguments(append(base.arguments(), "--http-proxy-listen", "127.0.0.1:8213"))
	require.Error(t, err)
}
