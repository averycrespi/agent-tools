package hostnorm_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostnorm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty", "", "", false},
		{"already-normal", "api.github.com", "api.github.com", false},
		{"uppercase", "API.GitHub.COM", "api.github.com", false},
		{"mixed-case-with-trailing-dot", "API.GitHub.COM.", "api.github.com", false},
		{"trailing-dot", "api.github.com.", "api.github.com", false},
		{"unicode-munchen", "münchen.de", "xn--mnchen-3ya.de", false},
		{"unicode-mixed", "MÜNCHEN.de", "xn--mnchen-3ya.de", false},
		{"ipv4", "127.0.0.1", "127.0.0.1", false},
		{"ipv4-trailing-dot", "127.0.0.1.", "127.0.0.1", false},
		{"ipv6-bracketed", "[::1]", "[::1]", false},
		{"ipv6-plain", "::1", "::1", false},
		{"ipv6-mixed-case", "[FE80::1]", "[FE80::1]", false},
		{"disallowed-null", "bad\u0000host", "", true},
		{"disallowed-space", "bad host", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hostnorm.Normalize(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNormalizeGlob(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"pure-literal", "api.github.com", "api.github.com", false},
		{"uppercase", "API.GITHUB.COM", "api.github.com", false},
		{"single-star-prefix", "*.github.com", "*.github.com", false},
		{"single-star-uppercase", "*.GITHUB.com", "*.github.com", false},
		{"double-star-prefix", "**.github.com", "**.github.com", false},
		{"double-star-uppercase", "**.GITHUB.COM", "**.github.com", false},
		{"mixed-segment-ascii", "api-*.github.com", "api-*.github.com", false},
		{"mixed-segment-uppercase", "API-*.GITHUB.com", "api-*.github.com", false},
		{"unicode-segment", "*.MÜNCHEN.de", "*.xn--mnchen-3ya.de", false},
		{"trailing-dot", "**.github.com.", "**.github.com", false},
		{"all-double-star", "**", "**", false},
		{"empty", "", "", false},
		{"disallowed-in-literal-segment", "bad\u0000.example.com", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hostnorm.NormalizeGlob(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNormalize_Idempotent(t *testing.T) {
	inputs := []string{
		"api.github.com",
		"MÜNCHEN.de",
		"127.0.0.1",
		"[::1]",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			once, err := hostnorm.Normalize(in)
			require.NoError(t, err)
			twice, err := hostnorm.Normalize(once)
			require.NoError(t, err)
			assert.Equal(t, once, twice, "Normalize must be idempotent")
		})
	}
}

func TestNormalizeGlob_Idempotent(t *testing.T) {
	inputs := []string{
		"**.github.com",
		"API-*.GITHUB.com",
		"*.MÜNCHEN.de",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			once, err := hostnorm.NormalizeGlob(in)
			require.NoError(t, err)
			twice, err := hostnorm.NormalizeGlob(once)
			require.NoError(t, err)
			assert.Equal(t, once, twice, "NormalizeGlob must be idempotent")
		})
	}
}
