//go:build e2e && browser

package e2e

import "testing"

func TestBrowserGitRouting(t *testing.T) {
	runHTTPBrowserScenario(t, "git-routing", "git_routing_complete")
}

func TestBrowserGitAdministrationAndTraffic(t *testing.T) {
	runHTTPBrowserScenario(t, "git", "git_complete")
}
