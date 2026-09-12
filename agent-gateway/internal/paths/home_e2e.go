//go:build e2e

package paths

import (
	"errors"
	"os"
)

const e2eAccountHomeEnvironment = "AGENT_GATEWAY_E2E_ACCOUNT_HOME"

func init() {
	currentUserHome = func() (string, error) {
		if _, exists := os.LookupEnv("MCP_GATEWAY_E2E_ACCOUNT_HOME"); exists {
			return "", errors.New("MCP_GATEWAY_E2E_ACCOUNT_HOME is retired; use AGENT_GATEWAY_E2E_ACCOUNT_HOME")
		}
		home := os.Getenv(e2eAccountHomeEnvironment)
		if home == "" {
			return "", errors.New("E2E account home is unavailable")
		}
		return home, nil
	}
}
