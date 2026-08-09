// Package auth owns role-credential lifecycle and the listener-specific checks.
package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// ProxyCredential returns the Proxy-Authorization header value a client should
// send: Basic base64("x:<token>").
func ProxyCredential(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+token))
}

// CheckProxyAuth reports whether a CONNECT or absolute-form request carries
// the expected agent credential. Basic and Bearer formats are supported.
func CheckProxyAuth(header, token string) bool {
	if header == "" || token == "" {
		return false
	}

	scheme, value, found := strings.Cut(header, " ")
	if !found {
		return false
	}
	value = strings.TrimSpace(value)

	switch {
	case strings.EqualFold(scheme, "Basic"):
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return false
		}
		_, password, ok := strings.Cut(string(decoded), ":")
		return ok && ConstantTimeEqual(password, token)
	case strings.EqualFold(scheme, "Bearer"):
		return ConstantTimeEqual(value, token)
	default:
		return false
	}
}

// ConstantTimeEqual compares two non-empty configured credentials without
// leaking attacker-controlled values through timing.
func ConstantTimeEqual(provided, expected string) bool {
	return expected != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// DashboardCookieName is the cookie set after an admin ?token= visit.
const DashboardCookieName = "http-broker-auth"

// CheckDashboardAuth reports whether a dashboard request carries the expected
// admin credential by Authorization header or dashboard cookie.
func CheckDashboardAuth(r *http.Request, token string) bool {
	if token == "" {
		return false
	}

	if header := r.Header.Get("Authorization"); header != "" {
		scheme, value, found := strings.Cut(header, " ")
		if found && strings.EqualFold(scheme, "Bearer") && ConstantTimeEqual(strings.TrimSpace(value), token) {
			return true
		}
	}
	if cookie, err := r.Cookie(DashboardCookieName); err == nil {
		return ConstantTimeEqual(cookie.Value, token)
	}
	return false
}
