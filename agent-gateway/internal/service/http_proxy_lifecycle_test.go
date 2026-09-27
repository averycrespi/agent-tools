//go:build darwin || linux

package service

import (
	"os"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestHTTPProxyFreshInstallAndInstalledLifecycle(t *testing.T) {
	for _, selected := range []string{"default", "", "127.0.0.1:8213", "legacy"} {
		t.Run(selected, func(t *testing.T) {
			f := newFixture(t)
			changes := Changes{}
			want := selected
			switch selected {
			case "default":
				want = contract.DefaultHTTPProxyAuthority
			case "legacy":
				want = ""
				changes.HTTPProxyListen = &want
			default:
				changes.HTTPProxyListen = &want
			}
			_, err := f.m.execute(t.Context(), "install", changes)
			require.NoError(t, err)
			d, data, _, err := f.m.read()
			require.NoError(t, err)
			require.Equal(t, want, d.HTTPProxyListen)
			if selected == "legacy" {
				d.argv = nil
				for _, arg := range d.arguments() {
					if arg != "--clear-http-proxy-listen" {
						d.argv = append(d.argv, arg)
					}
				}
				data, err = d.encode()
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(f.m.plist(), data, 0o600))
			}
			for _, operation := range []string{"update", "start", "restart"} {
				_, err = f.m.execute(t.Context(), operation, Changes{})
				require.NoError(t, err)
				got, after, _, readErr := f.m.read()
				require.NoError(t, readErr)
				require.Equal(t, data, after, "unchanged lifecycle must not rewrite definition")
				require.Equal(t, want, got.HTTPProxyListen)
			}
			_, err = f.m.execute(t.Context(), "update", Changes{LogLevel: ptr("info")})
			require.NoError(t, err)
			d, _, _, err = f.m.read()
			require.NoError(t, err)
			require.Equal(t, want, d.HTTPProxyListen)
			if want == "" {
				require.Contains(t, d.argv, "--clear-http-proxy-listen")
			}
			for _, replacement := range []string{"", "127.0.0.1:8214", ""} {
				_, err = f.m.execute(t.Context(), "update", Changes{HTTPProxyListen: &replacement})
				require.NoError(t, err)
				d, _, _, err = f.m.read()
				require.NoError(t, err)
				require.Equal(t, replacement, d.HTTPProxyListen)
				relevant, observationErr := relevantCommand(strings.Join(d.argv, " "), d)
				require.NoError(t, observationErr)
				require.True(t, relevant)
			}
		})
	}
}

func TestHTTPProxyLegacyAndExplicitDisabledParsing(t *testing.T) {
	args := []string{"/opt/bin/agent-gateway", "serve", "--data-dir", "/private/gateway", "--listen", contract.DefaultAuthority}
	for _, suffix := range [][]string{nil, {"--clear-http-proxy-listen"}} {
		parsed, err := parseArguments(append(append([]string(nil), args...), suffix...))
		require.NoError(t, err)
		require.Empty(t, parsed.HTTPProxyListen)
		require.Contains(t, parsed.arguments(), "--clear-http-proxy-listen")
	}
	_, err := parseArguments(append(args, "--clear-http-proxy-listen", "--http-proxy-listen", contract.DefaultHTTPProxyAuthority))
	require.ErrorContains(t, err, "conflicting")
}
