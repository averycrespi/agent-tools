package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/auth"
)

func TestProxyAcceptsOnlyAgentRole(t *testing.T) {
	store, err := auth.NewStore(auth.TokenSet{Agent: strings.Repeat("a", 64), Admin: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	proxy := New(Options{Auth: store})

	for _, tt := range []struct {
		name       string
		token      string
		wantStatus int
	}{
		{name: "agent bearer reaches request validation", token: strings.Repeat("a", 64), wantStatus: http.StatusBadRequest},
		{name: "agent basic reaches request validation", token: strings.Repeat("a", 64), wantStatus: http.StatusBadRequest},
		{name: "admin is rejected", token: strings.Repeat("b", 64), wantStatus: http.StatusProxyAuthRequired},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
			if strings.Contains(tt.name, "basic") {
				request.Header.Set("Proxy-Authorization", auth.ProxyCredential(tt.token))
			} else {
				request.Header.Set("Proxy-Authorization", "Bearer "+tt.token)
			}
			response := httptest.NewRecorder()
			proxy.ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
		})
	}
}
