package dashboard

import "testing"

// TestSafeRedirectTarget is the assertion the nolint on the redirect relies
// on: the target is always one of the known routes, never anything derived
// from the request. A path like "//evil.com" is a valid protocol-relative URL,
// so reflecting the request path would be an open redirect.
func TestSafeRedirectTarget(t *testing.T) {
	hostile := []string{
		"//evil.com",
		"///evil.com",
		"//evil.com/path",
		"https://evil.com",
		"/\\evil.com",
		"/../../etc/passwd",
		"/api/audit/../../../evil",
		"",
		"/unknown",
	}
	for _, path := range hostile {
		if got := safeRedirectTarget(path); got != "/" {
			t.Errorf("safeRedirectTarget(%q) = %q, want %q", path, got, "/")
		}
	}

	for path := range dashboardPaths {
		if got := safeRedirectTarget(path); got != path {
			t.Errorf("safeRedirectTarget(%q) = %q, want it preserved", path, got)
		}
	}
}

// TestRedirectTargetsAreRelativePaths guards the set itself: an absolute or
// protocol-relative entry would defeat the check.
func TestRedirectTargetsAreRelativePaths(t *testing.T) {
	for path := range dashboardPaths {
		if len(path) == 0 || path[0] != '/' {
			t.Errorf("route %q must start with /", path)
		}
		if len(path) > 1 && path[1] == '/' {
			t.Errorf("route %q is protocol-relative and would leave the origin", path)
		}
	}
}
