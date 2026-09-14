package service

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestServiceDarwinOwnedZombieProof(t *testing.T) {
	const pid, parent = 1234, 5678
	var zombie unix.KinfoProc
	zombie.Proc.P_pid = pid
	zombie.Proc.P_stat = 5
	zombie.Eproc.Ppid = parent
	zombie.Eproc.Pgid = pid
	require.True(t, ownedZombie(&zombie, pid, parent))
	require.False(t, ownedZombie(nil, pid, parent))
	require.False(t, ownedZombie(&zombie, 0, parent))
	require.False(t, ownedZombie(&zombie, pid, pid))
	for name, change := range map[string]func(*unix.KinfoProc){
		"live":          func(info *unix.KinfoProc) { info.Proc.P_stat = 2 },
		"unknown state": func(info *unix.KinfoProc) { info.Proc.P_stat = 0 },
		"wrong child":   func(info *unix.KinfoProc) { info.Proc.P_pid++ },
		"wrong parent":  func(info *unix.KinfoProc) { info.Eproc.Ppid++ },
		"wrong group":   func(info *unix.KinfoProc) { info.Eproc.Pgid++ },
	} {
		t.Run(name, func(t *testing.T) {
			info := zombie
			change(&info)
			require.False(t, ownedZombie(&info, pid, parent))
		})
	}
	require.ErrorIs(t, groupCleanupError(pid, syscall.EINVAL), syscall.EINVAL)
	require.ErrorIs(t, groupCleanupError(os.Getpid(), syscall.EPERM), syscall.EPERM)
	require.ErrorIs(t, groupCleanupError(0, syscall.EPERM), syscall.EPERM)
}

func TestServiceDarwinSingletonQueryRejectsMultipleMembers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	pidFile := filepath.Join(t.TempDir(), "owned-child")
	done := make(chan error, 1)
	go func() {
		_, _, err := runOwned(ctx, "/bin/sh", "-c", `sleep 20 & printf '%s' $$ > "$1"; wait`, "fixture", pidFile)
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.Error(t, err)
		case <-time.After(7 * time.Second):
			t.Fatal("owned utility cleanup did not finish")
		}
	})
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(data))
		return err == nil && pid > 1
	}, time.Second, 10*time.Millisecond)
	// The shell publishes after spawning its child and waits until cancellation.
	// A one-record group query must reject this partial snapshot, not silently
	// return its first member as proof of a singleton.
	_, err := unix.SysctlKinfoProc("kern.proc.pgrp", pid)
	require.ErrorIs(t, err, syscall.ENOMEM)
	err = groupCleanupError(pid, syscall.EPERM)
	require.ErrorIs(t, err, syscall.EPERM)
	require.ErrorIs(t, err, syscall.ENOMEM)
}
