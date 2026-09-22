package httpproxy

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProxyAuthenticationStandardClientGrammar(t *testing.T) {
	encode := func(s string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(s)) }
	token := "mgw_agent_" + strings.Repeat("a", 43)
	for _, value := range []string{"Bearer " + token, encode("agent:" + token)} {
		got, ok := proxyBearer([]string{value})
		require.True(t, ok)
		require.Equal(t, token, got)
	}
	for _, values := range [][]string{nil, {encode("agent:" + token), encode("agent:" + token)}, {encode("wrong:" + token)}, {encode("agent:")}, {encode("agent:x:y")}, {"Basic !"}, {"Digest x"}, {strings.Repeat("a", 1025)}} {
		_, ok := proxyBearer(values)
		require.False(t, ok)
	}
	recorder := httptest.NewRecorder()
	reject(recorder, http.StatusProxyAuthRequired)
	require.Equal(t, `Basic realm="Agent Gateway"`, recorder.Header().Get("Proxy-Authenticate"))
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.NotContains(t, recorder.Body.String(), token)
}
