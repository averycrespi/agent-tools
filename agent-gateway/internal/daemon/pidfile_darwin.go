//go:build darwin

package daemon

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const expectedComm = "agent-gateway"

// defaultVerifyComm runs `ps -p <pid> -o comm=` and checks whether the output
// matches "agent-gateway".
func defaultVerifyComm(pid int) (bool, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		// ps exits non-zero when the process does not exist — treat as stale.
		return false, nil
	}
	return strings.TrimSpace(string(out)) == expectedComm, nil
}
