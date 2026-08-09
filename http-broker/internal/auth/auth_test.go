package auth_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/auth"
)

func TestCheckProxyAuth(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"correct basic credential", auth.ProxyCredential(token), true},
		{"correct bearer", "Bearer " + token, true},
		{"lowercase basic scheme", strings.ToLower("Basic ") + base64.StdEncoding.EncodeToString([]byte("x:"+token)), true},
		{"any username is accepted", "Basic " + base64.StdEncoding.EncodeToString([]byte("anyone:"+token)), true},
		{"empty username", "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+token)), true},

		{"empty header", "", false},
		{"wrong token", "Bearer " + strings.Repeat("f", 64), false},
		{"truncated token", "Bearer " + token[:32], false},
		{"token with trailing junk", "Bearer " + token + "x", false},
		{"no scheme", token, false},
		{"unknown scheme", "Digest " + token, false},
		{"basic with no colon", "Basic " + base64.StdEncoding.EncodeToString([]byte(token)), false},
		{"basic with invalid base64", "Basic not-base64!!", false},
		{"password empty", "Basic " + base64.StdEncoding.EncodeToString([]byte("x:")), false},
		{"token in the username position", "Basic " + base64.StdEncoding.EncodeToString([]byte(token+":x")), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := auth.CheckProxyAuth(tc.header, token); got != tc.want {
				t.Errorf("case %q result = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestCheckProxyAuthRejectsEmptyConfiguredToken(t *testing.T) {
	// A misconfiguration that left the token empty must not turn into "every
	// request authenticates".
	if auth.CheckProxyAuth("Bearer ", "") {
		t.Error("an empty configured token must never authenticate")
	}
	if auth.CheckProxyAuth(auth.ProxyCredential(""), "") {
		t.Error("an empty configured token must never authenticate")
	}
}

func TestCheckDashboardAuth(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("bearer header", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "/api/audit", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		if !auth.CheckDashboardAuth(r, token) {
			t.Error("a correct Bearer header should authenticate")
		}
	})

	t.Run("cookie", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "/api/audit", nil)
		r.AddCookie(&http.Cookie{Name: auth.DashboardCookieName, Value: token})
		if !auth.CheckDashboardAuth(r, token) {
			t.Error("a correct cookie should authenticate")
		}
	})

	t.Run("no credential", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "/api/audit", nil)
		if auth.CheckDashboardAuth(r, token) {
			t.Error("a request with no credential must not authenticate")
		}
	})

	t.Run("wrong values", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "/api/audit", nil)
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("0", 64))
		r.AddCookie(&http.Cookie{Name: auth.DashboardCookieName, Value: "nope"})
		if auth.CheckDashboardAuth(r, token) {
			t.Error("wrong values must not authenticate")
		}
	})

	t.Run("proxy credential does not authenticate the dashboard", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "/api/audit", nil)
		r.Header.Set("Proxy-Authorization", auth.ProxyCredential(token))
		if auth.CheckDashboardAuth(r, token) {
			t.Error("Proxy-Authorization must not authenticate a dashboard request")
		}
	})
}

func TestProxyCredentialRoundTrips(t *testing.T) {
	const token = "abc123"
	header := auth.ProxyCredential(token)

	if !strings.HasPrefix(header, "Basic ") {
		t.Errorf("ProxyCredential = %q, want a Basic credential", header)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded) != "x:"+token {
		t.Error("decoded proxy credential did not contain the expected username and token")
	}
	if !auth.CheckProxyAuth(header, token) {
		t.Error("ProxyCredential output should satisfy CheckProxyAuth")
	}
}
