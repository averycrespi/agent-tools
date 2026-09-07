//go:build e2e && darwin

package main

import (
	"time"

	"golang.org/x/sys/unix"
)

func observeExit(pid int) <-chan error {
	result := make(chan error, 1)
	go func() {
		for {
			info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
			if err != nil {
				result <- err
				return
			}
			// Darwin's SZOMB is 5. x/sys exposes kinfo_proc but not waitid or SZOMB;
			// observing the zombie state keeps the direct child waitable and pins its PID.
			if info.Proc.P_stat == 5 {
				result <- nil
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	return result
}
