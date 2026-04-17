//go:build linux

package daemon

import (
	"fmt"
	"os"
	"strings"
)

const expectedComm = "agent-gateway"

// defaultVerifyComm reads /proc/<pid>/comm and checks whether it matches
// "agent-gateway".
func defaultVerifyComm(pid int) (bool, error) {
	path := fmt.Sprintf("/proc/%d/comm", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		// Process may have died between liveness check and comm read — treat
		// as stale rather than an error.
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read comm: %w", err)
	}
	return strings.TrimSpace(string(data)) == expectedComm, nil
}
