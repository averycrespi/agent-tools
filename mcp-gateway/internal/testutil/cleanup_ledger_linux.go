//go:build linux

package testutil

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func externalProcessIdentity(pid int) (string, error) {
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	closing := strings.LastIndex(string(contents), ") ")
	if closing < 0 {
		return "", fmt.Errorf("process stat is malformed")
	}
	fields := strings.Fields(string(contents)[closing+2:])
	if len(fields) <= 19 {
		return "", fmt.Errorf("process stat lacks start time")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", fmt.Errorf("process start time is invalid: %w", err)
	}
	return fields[19], nil
}

func revalidateExternalProcessGroup(pid, groupID int, identity string) bool {
	current, err := externalProcessIdentity(pid)
	if err != nil || current != identity {
		return false
	}
	actualGroup, err := syscall.Getpgid(pid)
	return err == nil && actualGroup == groupID && groupID == pid
}

func signalExternalProcessGroup(groupID int, signal syscall.Signal) error {
	return syscall.Kill(-groupID, signal)
}
