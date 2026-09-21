package service

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefinitionCanonicalPlist(t *testing.T) {
	d := definition{
		Settings: Settings{
			Binary:       "/Applications/Gateway & Tools/agent-gateway",
			DataDir:      "/Users/example/data <gateway>",
			Listen:       "127.0.0.1:8210",
			AllowedHosts: []string{"host.lima.internal", "host.docker.internal"},
		},
		Stdout: "/Users/example/logs/stdout.log",
		Stderr: "/Users/example/logs/stderr.log",
	}
	data, err := d.encode()
	require.NoError(t, err)
	require.Contains(t, string(data), `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`)
	for _, key := range []string{"RunAtLoad", "KeepAlive"} {
		require.Contains(t, string(data), "<key>"+key+"</key>\n    <true/>")
	}
	require.NotContains(t, string(data), "</true>")
	require.Contains(t, string(data), "Gateway &amp; Tools")
	require.Contains(t, string(data), "data &lt;gateway&gt;")
	decoded, err := decode(data)
	require.NoError(t, err)
	require.Equal(t, d.Settings, decoded.Settings)
	require.Equal(t, d.Stdout, decoded.Stdout)
	require.Equal(t, d.Stderr, decoded.Stderr)

	// Existing definitions remain readable so update can replace their encoding.
	legacy := bytes.ReplaceAll(data, []byte("<true/>"), []byte("<true></true>"))
	decoded, err = decode(legacy)
	require.NoError(t, err)
	require.Equal(t, d.Settings, decoded.Settings)
}
