package httpcredentials

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

func testDefinition() Definition {
	return Definition{Name: "Example API", Boundary: Boundary{Host: "api.example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}
}

func TestCredentialRecipeValidation(t *testing.T) {
	for _, header := range []string{"Authorization", "authorization", "X-API-Key", "API-Key", "X-Custom-Token"} {
		require.True(t, contract.ValidHTTPCredentialSecret(contract.HTTPCredentialRecipe{Header: header, Prefix: "Bearer "}, []byte("opaque-value")))
	}
	for _, header := range []string{"", "Host", "HOST", "cOnNeCtIoN", "Proxy-Authorization", "Transfer-Encoding", "Content-Length", "TE", "Trailer", "Upgrade", "Expect", "Sec-WebSocket-Key", "X-Forwarded-For", "Cookie", "Set-Cookie", "If-Match", "Range", "Access-Control-Allow-Origin", "x\r\ninjected", "bad header", "é", strings.Repeat("x", 129)} {
		t.Run(header, func(t *testing.T) {
			require.False(t, contract.ValidHTTPCredentialRecipe(contract.HTTPCredentialRecipe{Header: header}))
		})
	}
	for _, secret := range []string{"", "token\r\nX: value", " token", "token ", "\ttoken", "token\x00", "é", strings.Repeat("a", 4097)} {
		require.False(t, contract.ValidHTTPCredentialSecret(testDefinition().Recipe, []byte(secret)))
	}
	for _, prefix := range []string{" Bearer", "Bearer\t", "Bearer\r", "é", strings.Repeat("x", 129)} {
		require.False(t, contract.ValidHTTPCredentialRecipe(contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: prefix}))
	}
	require.True(t, contract.ValidHTTPCredentialSecret(testDefinition().Recipe, bytes.Repeat([]byte{'x'}, 4089)))
	require.False(t, contract.ValidHTTPCredentialSecret(testDefinition().Recipe, bytes.Repeat([]byte{'x'}, 4090)))
}

func TestCredentialBoundaryAndContainment(t *testing.T) {
	def := testDefinition()
	def.Boundary.Host = "*.Example.COM"
	_, err := Normalize(def)
	require.ErrorIs(t, err, ErrInvalid)
	def.Boundary.AllowWildcard = true
	def, err = Normalize(def)
	require.NoError(t, err)
	require.Equal(t, "*.example.com", def.Boundary.Host)
	ref := contract.HTTPRevisionRef{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Revision: 1}
	for _, tc := range []struct {
		host, scheme string
		port         uint16
		allowed      bool
	}{
		{"api.example.com", "https", 443, true}, {"*.api.example.com", "https", 443, true},
		{"example.com", "https", 443, false}, {"*.com", "https", 443, false},
		{"evil-example.com", "https", 443, false}, {"api.example.com", "https", 8443, false},
		{"api.example.com", "http", 443, false},
	} {
		p, compileErr := httppolicy.Compile(contract.HTTPPolicy{Version: 1, Type: contract.HTTPAllowRequests, Request: &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: tc.scheme, Host: tc.host, Port: tc.port}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}})
		if compileErr != nil {
			require.False(t, tc.allowed)
			continue
		}
		allowed, containErr := httppolicy.CredentialContains(def.PolicyCredential(ref, true), p)
		if tc.allowed {
			require.NoError(t, containErr)
		}
		require.Equal(t, tc.allowed, allowed, tc.host)
	}
	for _, host := range []string{"*.127.0.0.1", "*.::1", "*.*.example.com", "example.com.", "*.com"} {
		bad := def
		bad.Boundary.Host = host
		_, err = Normalize(bad)
		require.Error(t, err, host)
	}
}

func TestPinnedMaterialReplacesAllCaseVariantsWithoutPartialMutation(t *testing.T) {
	secret := []byte("private-canary")
	ref := contract.HTTPRevisionRef{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Revision: 7}
	material, err := newMaterial(ref, testDefinition(), secret)
	require.NoError(t, err)
	defer material.Clear()
	require.Equal(t, make([]byte, len(secret)), secret)
	require.Equal(t, ref, material.Reference())
	encoded, err := json.Marshal(material) //nolint:staticcheck // Assert that accidental JSON serialization cannot reveal material.
	require.NoError(t, err)
	require.Equal(t, "{}", string(encoded))
	for _, format := range []string{"%v", "%+v", "%#v"} {
		require.Equal(t, "HTTP credential material [redacted]", fmt.Sprintf(format, material))
	}
	original := http.Header{"authorization": {"stale"}, "AUTHORIZATION": {"stale2"}, "X-Unrelated": {"preserved"}}
	before := original.Clone()
	target, err := httppolicy.ParseRequest("https://api.example.com/path", "GET", "api.example.com", "", nil)
	require.NoError(t, err)
	out, err := material.Headers(target, original)
	require.NoError(t, err)
	require.Equal(t, http.Header{"Authorization": {"Bearer private-canary"}, "X-Unrelated": {"preserved"}}, out)
	require.Equal(t, before, original)
	out["X-Unrelated"][0] = "changed"
	require.Equal(t, "preserved", original.Get("X-Unrelated"))
	for _, raw := range []string{"http://api.example.com/path", "https://evil.example.com/path", "https://api.example.com:8443/path"} {
		host := strings.Split(strings.Split(raw, "://")[1], "/")[0]
		bad, parseErr := httppolicy.ParseRequest(raw, "GET", host, "", nil)
		require.NoError(t, parseErr)
		out, err = material.Headers(bad, original)
		require.ErrorIs(t, err, ErrInvalid)
		require.Nil(t, out)
		require.Equal(t, before, original)
	}
	original["cOnNeCtIoN"] = []string{"keep-alive, aUtHoRiZaTiOn"}
	out, err = material.Headers(target, original)
	require.ErrorIs(t, err, ErrInvalid)
	require.Nil(t, out)
	material.Clear()
	out, err = material.Headers(target, nil)
	require.ErrorIs(t, err, ErrInvalid)
	require.Nil(t, out)
}
