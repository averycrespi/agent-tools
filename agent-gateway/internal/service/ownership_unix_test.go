//go:build darwin || linux

package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestServiceUnrelatedExecutableCommandsDoNotBlock(t *testing.T) {
	for _, args := range []string{"status", "--data-dir /another/root serve --listen 127.0.0.1:9999", "serve --listen 127.0.0.1:9999 --data-dir=/another/root", "--data-dir ROOT status", "serve --data-dir ROOT-old --listen 127.0.0.1:9999"} {
		t.Run(args, func(t *testing.T) {
			f := newFixture(t)
			f.install(t)
			f.running = true
			d, _, _, err := f.m.read()
			require.NoError(t, err)
			f.command = f.m.executable + " " + strings.ReplaceAll(args, "ROOT", d.DataDir)
			owners, err := f.m.residual(t.Context(), d)
			require.NoError(t, err)
			require.Empty(t, owners)
		})
	}
}
func TestServiceLiteralDelimiterPathRemainsManageable(t *testing.T) {
	f := newFixture(t)
	oldBinary := f.m.executable
	f.m.executable += " "
	require.NoError(t, os.Rename(oldBinary, f.m.executable))
	root := filepath.Join(f.m.home, "literal --listen path ")
	_, err := f.m.execute(t.Context(), "install", Changes{DataDir: &root})
	require.NoError(t, err)
	f.loaded = true
	f.running = true
	_, err = f.m.execute(t.Context(), "stop", Changes{})
	require.NoError(t, err)
	require.Equal(t, []string{"bootout"}, f.mutations)
}
func TestServiceUnknownAndReusedProcessIdentityRefusesReplacement(t *testing.T) {
	for _, failure := range []string{"process", "reuse"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.install(t)
			f.loaded = true
			f.running = true
			f.fail = failure
			before, err := os.ReadFile(f.m.plist())
			require.NoError(t, err)
			result, err := f.m.execute(t.Context(), "update", Changes{LogLevel: ptr("debug")})
			require.Error(t, err)
			require.Equal(t, "unknown", result.Launchd)
			after, err := os.ReadFile(f.m.plist())
			require.NoError(t, err)
			require.Equal(t, before, after)
			if failure == "reuse" {
				require.Equal(t, []string{"bootout"}, f.mutations)
			} else {
				require.Empty(t, f.mutations)
			}
		})
	}
}
func TestServiceInstallationFenceCoversPublication(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	d, _, _, err := f.m.read()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(d.DataDir, 0700))
	path := filepath.Join(d.DataDir, "gateway.lock")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	f.loaded = true
	f.running = true
	original := f.m.publish
	published := false
	finalInspection := false
	originalRun := f.m.run
	f.m.run = func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
		if name == "/bin/launchctl" && published {
			fd, e := unix.Open(path, unix.O_RDONLY, 0)
			require.NoError(t, e)
			lockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
			if args[0] == "print" {
				finalInspection = true
				require.ErrorIs(t, lockErr, unix.EWOULDBLOCK, "final launchd inspection remains fenced")
			}
			if args[0] == "bootstrap" {
				require.NoError(t, lockErr, "release specifically for launch handoff")
			}
			_ = unix.Close(fd)
		}
		return originalRun(ctx, name, args...)
	}
	f.m.publish = func(path string, data []byte, replace bool) (bool, error) {
		fd, e := unix.Open(filepath.Join(d.DataDir, "gateway.lock"), unix.O_RDONLY, 0)
		require.NoError(t, e)
		defer func() { _ = unix.Close(fd) }()
		require.ErrorIs(t, unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB), unix.EWOULDBLOCK, "stopped installation must remain fenced during publication")
		published = true
		return original(path, data, replace)
	}
	_, err = f.m.execute(t.Context(), "update", Changes{LogLevel: ptr("debug")})
	require.NoError(t, err)
	require.True(t, finalInspection)
	fd, err := unix.Open(path, unix.O_RDONLY, 0)
	require.NoError(t, err)
	defer func() { _ = unix.Close(fd) }()
	require.NoError(t, unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB), "fence must be released for launched Gateway")
}
