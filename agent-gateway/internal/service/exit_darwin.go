package service

import "golang.org/x/sys/unix"

func childExited(pid int) (bool, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	// Darwin sys/proc.h defines SZOMB as 5. The direct child remains unreaped.
	return info.Proc.P_stat == 5, nil
}
