//go:build e2e && browser

package e2e

import "testing"

func TestBrowserGitAdministrationAndTraffic(t *testing.T) {
	runHTTPBrowserScenario(t, "git", "git_complete")
}
