package httpboundary

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExternalCallbackRejectsWrongAuthorityPathAndBodyBeforeDispatch(t *testing.T) {
	calls := 0
	boundary, err := New(Options{Authority: "0.0.0.0:8210", ExternalOrigin: "https://gateway.example:8443", AllowedHosts: []string{"inside.example"}, Next: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(204) })})
	require.NoError(t, err)
	for _, test := range []struct {
		host, path, body, header string
		status                   int
	}{
		{"gateway.example:8443", "/oauth/callback?state=fixture", "", "", 204},
		{"gateway.example", "/oauth/callback", "", "", 421},
		{"gateway.example:8444", "/oauth/callback", "", "", 421},
		{"127.0.0.1:8210", "/oauth/callback", "", "", 421},
		{"inside.example", "/oauth/callback", "", "", 421},
		{"gateway.example:8443", "/oauth/%63allback", "", "", 400},
		{"gateway.example:8443", "/oauth/callback", "body", "", 400},
		{"gateway.example:8443", "/oauth/callback", "", "Forwarded", 400},
		{"gateway.example:8443", "/oauth/callback", "", "X-Forwarded-Host", 400},
	} {
		request := httptest.NewRequest("GET", test.path, strings.NewReader(test.body))
		request.Host = test.host
		if test.header != "" {
			request.Header.Set(test.header, "gateway.example:8443")
		}
		before := calls
		response := httptest.NewRecorder()
		boundary.ServeHTTP(response, request)
		require.Equal(t, test.status, response.Code)
		if test.status != 204 {
			require.Equal(t, before, calls)
		}
	}
}
