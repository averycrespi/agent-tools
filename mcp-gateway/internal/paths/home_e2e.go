//go:build e2e

package paths

import (
	"errors"
	"os"
)

const e2eAccountHomeEnvironment = "MCP_GATEWAY_E2E_ACCOUNT_HOME"

func init() {
	currentUserHome = func() (string, error) {
		home := os.Getenv(e2eAccountHomeEnvironment)
		if home == "" {
			return "", errors.New("E2E account home is unavailable")
		}
		return home, nil
	}
}
