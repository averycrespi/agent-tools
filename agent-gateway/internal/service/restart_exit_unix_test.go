//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestServiceRestartWaitsForTrackedZombie(t *testing.T) {
	for _, executable := range []string{"(agent-gateway)", "<defunct>", "", "exact"} {
		t.Run(executable, func(t *testing.T) {
			f := newFixture(t)
			f.install(t)
			f.loaded, f.running = true, true
			original := f.m.run
			observations := 0
			f.m.run = func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
				if len(f.mutations) == 1 {
					if name == "/bin/launchctl" && args[0] == "print" {
						observations++
					}
					if name == "/bin/ps" && observations == 1 {
						if args[0] == "-axwwo" {
							// A tracked zombie may still have its exact path in inventory.
							return []byte(fmt.Sprintf("%d 123456 %s\n", f.m.uid, f.m.executable)), 0, nil
						}
						require.Equal(t, "uid=,lstart=,state=,comm=", args[len(args)-1], "do not establish fresh ownership from zombie arguments")
						comm := executable
						if comm == "exact" {
							comm = f.m.executable
						}
						return []byte(fmt.Sprintf("%d Mon Sep 14 00:00:00 2026 Zs %s\n", f.m.uid, comm)), 0, nil
					}
				}
				if name == "/bin/launchctl" && args[0] == "bootstrap" {
					require.Greater(t, observations, 1, "zombie is not absence")
				}
				return original(ctx, name, args...)
			}
			result, err := f.m.execute(t.Context(), "restart", Changes{})
			require.NoError(t, err)
			require.Equal(t, "launch-accepted", result.Launchd)
			require.Equal(t, []string{"bootout", "bootstrap"}, f.mutations)
		})
	}
}

func TestServiceRestartExitIdentityRefusals(t *testing.T) {
	for _, failure := range []string{"live-mismatch", "live-defunct", "zombie-path-mismatch", "uid", "start", "unknown-state", "malformed-state", "malformed-start", "multiple-records", "missing-state", "inspection", "nonzero", "persistent-zombie", "initial-zombie"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.install(t)
			f.loaded, f.running = true, true
			before, err := os.ReadFile(f.m.plist())
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			original := f.m.run
			f.m.run = func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
				if name != "/bin/ps" || args[0] != "-ww" || args[len(args)-1] == "command=" || (len(f.mutations) == 0 && failure != "initial-zombie") {
					return original(ctx, name, args...)
				}
				uid, start, state, comm := strconv.Itoa(f.m.uid), "Mon Sep 14 00:00:00 2026", "Z", "(agent-gateway)"
				switch failure {
				case "live-mismatch":
					state = "S"
				case "live-defunct":
					state, comm = "S", "<defunct>"
				case "zombie-path-mismatch":
					comm = "/another/agent-gateway"
				case "uid":
					uid = strconv.Itoa(f.m.uid + 1)
				case "start":
					start = "Mon Sep 14 00:00:01 2026"
				case "unknown-state":
					state = "?E"
				case "malformed-state":
					state = "Zombie"
				case "malformed-start":
					start = "Mon Sep 99 00:00:00 2026"
				case "multiple-records":
					comm += "\n" + uid + " " + start + " Z " + comm
				case "missing-state":
					state = ""
				case "inspection":
					return nil, -1, errors.New("injected inspection failure")
				case "nonzero":
					return []byte("unavailable"), 1, nil
				case "persistent-zombie":
					cancel() // Exercise the stop deadline branch without a 30-second sleep.
				}
				return []byte(fmt.Sprintf("%s %s %s %s\n", uid, start, state, comm)), 0, nil
			}
			result, err := f.m.execute(ctx, "update", Changes{LogLevel: ptr("debug")})
			require.Error(t, err)
			if failure == "persistent-zombie" {
				require.ErrorContains(t, err, "stop unconfirmed within 30 seconds")
			}
			require.Equal(t, "unknown", result.Launchd)
			if failure == "initial-zombie" {
				require.Empty(t, f.mutations)
			} else {
				require.Equal(t, []string{"bootout"}, f.mutations)
			}
			after, err := os.ReadFile(f.m.plist())
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestServiceRestartExitStillRequiresOwnershipFences(t *testing.T) {
	for _, fence := range []string{"job", "residual", "lock"} {
		t.Run(fence, func(t *testing.T) {
			f := newFixture(t)
			f.install(t)
			f.loaded, f.running = true, true
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if fence == "lock" {
				d, _, _, err := f.m.read()
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(d.DataDir, 0700))
				file, err := os.OpenFile(filepath.Join(d.DataDir, "gateway.lock"), os.O_CREATE|os.O_RDWR, 0600)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, file.Close()) })
				require.NoError(t, unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB))
			}
			original := f.m.run
			observations := 0
			f.m.run = func(ctx context.Context, name string, args ...string) ([]byte, int, error) {
				if len(f.mutations) == 1 {
					if name == "/bin/launchctl" && args[0] == "print" {
						observations++
						if observations > 1 && fence == "job" {
							f.loaded = true
							cancel()
						}
					}
					if name == "/bin/ps" && args[0] == "-ww" && args[2] == "123456" {
						if observations == 1 {
							return []byte(fmt.Sprintf("%d Mon Sep 14 00:00:00 2026 Z (agent-gateway)\n", f.m.uid)), 0, nil
						}
						return nil, 1, nil
					}
					if name == "/bin/ps" && observations > 1 && fence == "residual" {
						f.running = true
						cancel()
						data, code, err := original(ctx, name, args...)
						return []byte(strings.ReplaceAll(string(data), "123456", "654321")), code, err
					}
				}
				return original(ctx, name, args...)
			}
			_, err := f.m.execute(ctx, "restart", Changes{})
			require.Error(t, err)
			if fence == "lock" {
				require.ErrorContains(t, err, "installation still has an owner")
			} else {
				require.ErrorContains(t, err, "stop unconfirmed")
			}
			require.Equal(t, []string{"bootout"}, f.mutations)
		})
	}
}

func TestServiceProcessColumnsAndLiveStateChanges(t *testing.T) {
	f := newFixture(t)
	f.m.uid = 2026 // The year also occurs before lstart; substring extraction is unsafe.
	f.m.run = func(_ context.Context, name string, args ...string) ([]byte, int, error) {
		require.Equal(t, "/bin/ps", name)
		require.Equal(t, []string{"-ww", "-p", "123456", "-o", "uid=,lstart=,state=,comm="}, args)
		return []byte("2026 Mon Sep 14 00:00:00 2026 R+ /literal  executable \n"), 0, nil
	}
	p, err := f.m.process(t.Context(), "123456", "/literal  executable ")
	require.NoError(t, err)
	require.Equal(t, "Mon Sep 14 00:00:00 2026", p.Start)
	require.Equal(t, "/literal  executable ", p.Executable)
}
