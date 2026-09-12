//go:build darwin

package testutil

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func externalProcessIdentity(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
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
