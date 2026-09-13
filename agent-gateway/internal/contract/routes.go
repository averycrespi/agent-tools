// Package contract defines the fixed, test-visible product boundary shared by Gateway packages.
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
	{Pattern: "/api/v2/admin-sessions", Methods: []string{"POST"}, Authority: AuthorityAdminBearer},
	{Pattern: "/api/v2/admin-sessions/current", Methods: []string{"DELETE", "POST"}, Authority: AuthorityAdminSession},
	{Pattern: "/api/v2/admin-credentials", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/admin-credentials/{id}", Methods: []string{"DELETE", "GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/system-status", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/backups", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/backups/{id}", Methods: []string{"DELETE", "GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/events", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/servers", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/servers/{id}", Methods: []string{"DELETE", "GET", "PATCH"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/servers/{id}/operations", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/servers/{id}/operations/{operation_id}", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/servers/{id}/credential-replacements", Methods: []string{"POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/servers/{id}/oauth-flows", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/servers/{id}/oauth-flows/{flow_id}", Methods: []string{"DELETE", "GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/catalog", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/servers/{id}/descriptors", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/mcp/servers/{id}/descriptors/{tool_id}", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/principals", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/principals/{id}", Methods: []string{"GET", "PATCH"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/principals/{id}/credential", Methods: []string{"DELETE", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/grants", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/grants/{id}", Methods: []string{"DELETE", "GET", "PATCH"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/grant-constraints/validate", Methods: []string{"POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/grant-requests", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/grant-requests/{id}", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/grant-requests/{id}/approve", Methods: []string{"POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/grant-requests/{id}/reject", Methods: []string{"POST"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/invocations", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/invocations/{id}", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/admin-authority", Methods: []string{"GET"}, Authority: AuthorityAdminBearer},
	{Pattern: "/api/v2/admin-credentials/{id}/rotation-completion", Methods: []string{"POST"}, Authority: AuthorityAdminBearer},
	{Pattern: "/api/v2/audit-events", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	{Pattern: "/api/v2/audit-events/{id}", Methods: []string{"GET"}, Authority: AuthorityAdmin},
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

func AuthorityForMethod(route Route, method string) CredentialAuthority {
	if route.Pattern == "/api/v2/events" && method == "POST" {
		return AuthorityAdminSession
	}
	return route.Authority
}

func joinMethods(methods []string) string {
	return strings.Join(methods, ", ")
}

func matchesRoute(pattern, path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") {
		return false
	}
	if pattern == "/assets/*" {
		return strings.HasPrefix(path, "/assets/") && len(path) > len("/assets/")
	}
	patternSegments := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	pathSegments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(patternSegments) != len(pathSegments) {
		return false
	}
	for index, segment := range patternSegments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			if pathSegments[index] == "" {
				return false
			}
			continue
		}
		if segment != pathSegments[index] {
			return false
		}
	}
	return true
}
