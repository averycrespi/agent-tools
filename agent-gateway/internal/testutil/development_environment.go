package testutil

import (
	"fmt"
	"strings"
)

// CheckDevelopmentEnvironment rejects retired fixture controls before a runner
// can silently skip a fixture or discard its inherited cleanup ownership.
// Client-facing compatibility exports are not developer controls.
func CheckDevelopmentEnvironment(environment []string) error {
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "STRESS_COUNT", "DEMO_LISTEN", "DEMO_DATASET", "TEST_JSON", "DEMO_TEST_SCENARIO", "RESTART_CALLS", "RESTART_MODE", "RESTART_STOPPED", "MCP_FALLBACK_PROBE_PROCESS":
			return fmt.Errorf("%s is retired; use AGENT_GATEWAY_%s", name, name)
		case "GATEWAY_BUILD_DIR", "GATEWAY_INSTALL_DIR", "GATEWAY_PLUTIL_FIXTURE", "GATEWAY_TEST_ACCOUNT_PLIST", "GATEWAY_TEST_GOPATH", "GATEWAY_TEST_PLATFORM", "GATEWAY_TEST_FORBIDDEN", "GATEWAY_TEST_EXECUTABLE":
			return fmt.Errorf("%s is retired; use AGENT_%s", name, name)
		}
		if strings.HasPrefix(name, "MCP_GATEWAY_") && name != "MCP_GATEWAY_ENDPOINT" && name != "MCP_GATEWAY_AGENT_TOKEN" {
			return fmt.Errorf("%s is retired; use AGENT_GATEWAY_%s", name, strings.TrimPrefix(name, "MCP_GATEWAY_"))
		}
	}
	return nil
}
