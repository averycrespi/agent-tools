// Package contract defines the fixed, test-visible S1 boundary shared by later Gateway packages.
package contract

import "strings"

const (
	DefaultAuthority = "127.0.0.1:8210"
	CanonicalOrigin  = "http://127.0.0.1:8210"
)

type CredentialAuthority string

const (
	AuthorityPublic       CredentialAuthority = "public"
	AuthorityAgent        CredentialAuthority = "agent"
	AuthorityOAuthState   CredentialAuthority = "oauth_state"
	AuthorityAdminBearer  CredentialAuthority = "admin_bearer"
	AuthorityAdminSession CredentialAuthority = "admin_session"
	AuthorityAdmin        CredentialAuthority = "admin_bearer_or_session"
)

type Route struct {
	Pattern   string
	Methods   []string
	Authority CredentialAuthority
}

var routes = []Route{
	{Pattern: "/", Methods: []string{"GET"}, Authority: AuthorityPublic},
	{Pattern: "/assets/*", Methods: []string{"GET"}, Authority: AuthorityPublic},
	{Pattern: "/livez", Methods: []string{"GET"}, Authority: AuthorityPublic},
	{Pattern: "/readyz", Methods: []string{"GET"}, Authority: AuthorityPublic},
	{Pattern: "/mcp", Methods: []string{"DELETE", "GET", "POST"}, Authority: AuthorityAgent},
	{Pattern: "/oauth/callback", Methods: []string{"GET"}, Authority: AuthorityOAuthState},
	{Pattern: "/api/v1/admin-sessions", Methods: []string{"POST"}, Authority: AuthorityAdminBearer},
	{Pattern: "/api/v1/admin-sessions/current", Methods: []string{"DELETE"}, Authority: AuthorityAdminSession},
	{Pattern: "/api/v1/admin-credentials", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v1/admin-credentials/{id}", Methods: []string{"DELETE", "GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v1/system-status", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v1/backups", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v1/backups/{id}", Methods: []string{"DELETE", "GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v1/events", Methods: []string{"GET"}, Authority: AuthorityAdmin},
}

func Routes() []Route {
	result := make([]Route, len(routes))
	for index, route := range routes {
		result[index] = route
		result[index].Methods = append([]string(nil), route.Methods...)
	}
	return result
}

func RouteForPath(path string) (Route, bool) {
	for _, route := range routes {
		if matchesRoute(route.Pattern, path) {
			route.Methods = append([]string(nil), route.Methods...)
			return route, true
		}
	}
	return Route{}, false
}

func (route Route) Allow() string {
	return joinMethods(route.Methods)
}

func joinMethods(methods []string) string {
	return strings.Join(methods, ", ")
}

func matchesRoute(pattern, path string) bool {
	switch pattern {
	case "/assets/*":
		return strings.HasPrefix(path, "/assets/") && len(path) > len("/assets/")
	case "/api/v1/admin-credentials/{id}":
		return matchesOneSegment(path, "/api/v1/admin-credentials/")
	case "/api/v1/backups/{id}":
		return matchesOneSegment(path, "/api/v1/backups/")
	default:
		return path == pattern
	}
}

func matchesOneSegment(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(path, prefix)
	return remainder != "" && !strings.Contains(remainder, "/")
}
