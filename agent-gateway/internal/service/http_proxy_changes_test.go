//go:build darwin || linux

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPProxyServiceUpdatePreservesOrExplicitlyClears(t *testing.T) {
	selected := Settings{Binary: "/opt/bin/agent-gateway", DataDir: "/private/gateway", Listen: "127.0.0.1:8210", HTTPProxyListen: "127.0.0.1:8212"}
	require.Equal(t, selected, apply(selected, Changes{}))
	replacement := "127.0.0.1:8213"
	changed := apply(selected, Changes{HTTPProxyListen: &replacement})
	require.Equal(t, replacement, changed.HTTPProxyListen)
	cleared := ""
	changed = apply(selected, Changes{HTTPProxyListen: &cleared})
	require.Empty(t, changed.HTTPProxyListen)
	require.NotContains(t, changed.arguments(), "--http-proxy-listen")
	require.True(t, observationFlag("--http-proxy-listen", replacement))
	require.False(t, observationFlag("--http-proxy-listen", "0.0.0.0:8212"))
}
