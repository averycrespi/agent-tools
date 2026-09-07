//go:build e2e && linux

package main

import (
	"errors"

	"golang.org/x/sys/unix"
)

func observeExit(pid int) <-chan error {
	result := make(chan error, 1)
	go func() {
		var info unix.Siginfo
		for {
			err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			result <- err
			return
		}
	}()
	return result
}
