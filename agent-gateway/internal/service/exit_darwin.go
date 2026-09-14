package service

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func childExited(pid int) (bool, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	// Darwin sys/proc.h defines SZOMB as 5. The direct child remains unreaped.
	return info.Proc.P_stat == 5, nil
}

func groupCleanupError(pid int, signalErr error) error {
	if !errors.Is(signalErr, syscall.EPERM) || pid <= 1 || pid == os.Getpid() {
		return signalErr
	}
	// Darwin killpg1 excludes zombies and can return EPERM for a zombie-only
	// group. Never accept EPERM alone: prove the unreaped child is the group's
	// sole member. This fixed one-record query includes zombies and fails with
	// ENOMEM for additional members; unlike SysctlKinfoProcSlice it never retries
	// a growing process table. Multiple members or unavailable proof fail closed.
	info, err := unix.SysctlKinfoProc("kern.proc.pgrp", pid)
	if err != nil {
		return errors.Join(signalErr, fmt.Errorf("owned utility singleton group inspection failed: %w", err))
	}
	if !ownedZombie(info, pid, os.Getpid()) {
		return errors.Join(signalErr, errors.New("owned utility group is not an exited singleton"))
	}
	return nil
}

func ownedZombie(info *unix.KinfoProc, pid, parent int) bool {
	return info != nil && pid > 1 && pid != parent && int(info.Proc.P_pid) == pid &&
		int(info.Eproc.Ppid) == parent && int(info.Eproc.Pgid) == pid && info.Proc.P_stat == 5
}
