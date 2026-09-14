//go:build darwin || linux

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fixture struct {
	m         manager
	mu        sync.Mutex
	loaded    bool
	running   bool
	mutations []string
	fail      string
	command   string
	identity  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home := filepath.Join(root, "account & <home> 'with spaces")
	require.NoError(t, os.Mkdir(home, 0700))
	binary := filepath.Join(home, "gateway & binary")
	require.NoError(t, os.WriteFile(binary, []byte("fixture"), 0700))
	f := &fixture{}
	f.m = manager{home: home, uid: os.Getuid(), executable: binary, publish: publish, probe: func(context.Context, string) string { return "not-ready" }}
	f.m.run = f.run
	return f
}
func (f *fixture) run(_ context.Context, name string, args ...string) ([]byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "/bin/ps" {
		if args[0] == "-axwwo" {
			if f.running {
				return []byte(fmt.Sprintf("%d 123456 %s\n", f.m.uid, f.m.executable)), 0, nil
			}
			return []byte("0 1 /sbin/init\n"), 0, nil
		}
		if !f.running {
			return nil, 1, nil
		}
		d, _, _, err := f.m.read()
		if err != nil {
			return nil, -1, err
		}
		if args[len(args)-1] == "command=" {
			if f.command != "" {
				return []byte(f.command + "\n"), 0, nil
			}
			return []byte(strings.Join(d.argv, " ") + "\n"), 0, nil
		}
		if f.fail == "process" {
			return []byte("unknown process identity"), 0, nil
		}
		identity := f.identity
		if identity == "" {
			identity = "Mon Sep 14 00:00:00 2026"
		}
		return []byte(fmt.Sprintf("%d %s S %s\n", f.m.uid, identity, d.Binary)), 0, nil
	}
	if args[0] == "print" {
		if f.fail == "inspect" {
			return []byte("permission denied"), 1, nil
		}
		if !f.loaded {
			return []byte("Could not find service \"" + Label + "\" in domain for user"), 113, nil
		}
		d, _, _, err := f.m.read()
		if err != nil {
			return []byte("loaded"), 0, nil
		}
		state := "not running"
		pid := ""
		if f.running {
			state = "running"
			pid = "\tpid = 123456\n"
		}
		return []byte(fmt.Sprintf("%s = {\n\tpath = %s\n\tprogram = %s\n\tstate = %s\n%s\targuments = {\n%s\n\t}\n}\n", f.m.target(), f.m.plist(), d.Binary, state, pid, strings.Join(d.argv, "\n"))), 0, nil
	}
	f.mutations = append(f.mutations, args[0])
	if f.fail == args[0] {
		return nil, -1, errors.New("injected uncertain utility")
	}
	if args[0] == "bootout" && f.fail != "survive" {
		f.loaded = false
		f.running = false
	}
	if args[0] == "bootout" && f.fail == "reuse" {
		f.running = true
		f.identity = "Mon Sep 14 00:00:01 2026"
	}
	if args[0] == "bootstrap" {
		f.loaded = true
		f.running = true
	}
	return nil, 0, nil
}
func (f *fixture) install(t *testing.T) {
	t.Helper()
	r, e := f.m.execute(t.Context(), "install", Changes{})
	require.NoError(t, e)
	require.True(t, r.Installed)
	require.Equal(t, "unloaded", r.Launchd)
	require.Empty(t, f.mutations)
}
func ptr(s string) *string { return &s }

func TestServiceInstallLiteralAndRefusal(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	d, data, _, err := f.m.read()
	require.NoError(t, err)
	require.Equal(t, f.m.executable, d.Binary)
	require.Contains(t, string(data), "&amp;")
	require.Contains(t, string(data), "&lt;")
	_, err = os.Lstat(d.DataDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, inspect(d.Stdout, f.m.uid, false, true))
	require.NoError(t, inspect(d.Stderr, f.m.uid, false, true))
	_, err = f.m.execute(t.Context(), "install", Changes{})
	require.Error(t, err)
	after, e := os.ReadFile(f.m.plist())
	require.NoError(t, e)
	require.Equal(t, data, after)
}
func TestServiceUpdateTransitions(t *testing.T) {
	for _, state := range []string{"unloaded", "running", "loaded-exited"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			f.install(t)
			f.loaded = state != "unloaded"
			f.running = state == "running"
			hosts := []string{"first.example", "second.example"}
			r, err := f.m.execute(t.Context(), "update", Changes{LogLevel: ptr("debug"), AllowedHosts: &hosts})
			require.NoError(t, err)
			d, _, _, err := f.m.read()
			require.NoError(t, err)
			require.Equal(t, hosts, d.AllowedHosts)
			require.Equal(t, "debug", d.LogLevel)
			require.Equal(t, f.m.executable, d.Binary)
			if state == "unloaded" {
				require.Empty(t, f.mutations)
				require.Equal(t, "unloaded", r.Launchd)
			} else {
				require.Equal(t, []string{"bootout", "bootstrap"}, f.mutations)
			}
			before := len(f.mutations)
			_, err = f.m.execute(t.Context(), "update", Changes{})
			require.NoError(t, err)
			require.Len(t, f.mutations, before)
			hosts = []string{"replacement.example"}
			_, err = f.m.execute(t.Context(), "update", Changes{AllowedHosts: &hosts})
			require.NoError(t, err)
			d, _, _, err = f.m.read()
			require.NoError(t, err)
			require.Equal(t, hosts, d.AllowedHosts)
			require.Equal(t, "debug", d.LogLevel)
			hosts = nil
			_, err = f.m.execute(t.Context(), "update", Changes{AllowedHosts: &hosts})
			require.NoError(t, err)
			d, _, _, err = f.m.read()
			require.NoError(t, err)
			require.Empty(t, d.AllowedHosts)
		})
	}
}
func TestServiceFailureStates(t *testing.T) {
	for _, failure := range []string{"invalid", "publish", "bootstrap", "bootout", "inspect"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.install(t)
			f.loaded = true
			f.running = true
			before, err := os.ReadFile(f.m.plist())
			require.NoError(t, err)
			changes := Changes{LogLevel: ptr("debug")}
			switch failure {
			case "invalid":
				changes.Listen = ptr("0.0.0.0:8210")
			case "publish":
				f.m.publish = func(string, []byte, bool) (bool, error) { return false, errors.New("injected publication failure") }
			default:
				f.fail = failure
			}
			r, err := f.m.execute(t.Context(), "update", changes)
			require.Error(t, err)
			after, e := os.ReadFile(f.m.plist())
			require.NoError(t, e)
			switch failure {
			case "bootstrap":
				require.NotEqual(t, before, after)
				require.Equal(t, "debug", r.Settings.LogLevel)
				require.Equal(t, "unknown", r.Launchd)
				require.Equal(t, []string{"bootout", "bootstrap"}, f.mutations)
			case "publish":
				require.Equal(t, before, after)
				require.Equal(t, "unloaded", r.Launchd)
				require.Equal(t, []string{"bootout"}, f.mutations)
			case "bootout":
				require.Equal(t, before, after)
				require.Equal(t, []string{"bootout"}, f.mutations)
			default:
				require.Equal(t, before, after)
				require.Empty(t, f.mutations)
				require.True(t, f.running)
			}
		})
	}
}
func TestServiceLifecycleAndUninstallPreservation(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	d, _, _, err := f.m.read()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(d.DataDir, 0700))
	sentinel := filepath.Join(d.DataDir, "preserved")
	require.NoError(t, os.WriteFile(sentinel, []byte("state"), 0600))
	for _, verb := range []string{"stop", "start", "start", "restart", "stop", "stop", "restart", "uninstall"} {
		_, err = f.m.execute(t.Context(), verb, Changes{})
		require.NoError(t, err, verb)
	}
	require.Equal(t, []string{"bootstrap", "bootout", "bootstrap", "bootout", "bootstrap", "bootout"}, f.mutations)
	_, err = os.Lstat(f.m.plist())
	require.ErrorIs(t, err, os.ErrNotExist)
	for _, path := range []string{d.Binary, d.Stdout, d.Stderr, sentinel} {
		_, err = os.Stat(path)
		require.NoError(t, err)
	}
}
func TestServiceConcurrentMutationAndUnsafeFiles(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	release, err := f.m.lock()
	require.NoError(t, err)
	_, err = f.m.execute(t.Context(), "update", Changes{LogLevel: ptr("debug")})
	require.ErrorContains(t, err, "another canonical service mutation")
	release()
	require.Empty(t, f.mutations)
	require.NoError(t, os.Chmod(f.m.plist(), 0644))
	_, err = f.m.execute(t.Context(), "restart", Changes{})
	require.Error(t, err)
	require.Empty(t, f.mutations)
}
func TestServiceDefinitionClosedAndPreserved(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	d, _, _, err := f.m.read()
	require.NoError(t, err)
	d.LogLevel = "info"
	d.Output = "json"
	d.argv = nil
	data, err := d.encode()
	require.NoError(t, err)
	decoded, err := decode(data)
	require.NoError(t, err)
	require.Equal(t, d.Settings, decoded.Settings)
	for _, bad := range [][]byte{append(append([]byte(nil), data...), data...), bytes.Replace(data, []byte("<key>Label</key>"), []byte("<key>Unknown</key>"), 1), bytes.Replace(data, []byte("<true></true>"), []byte("<false></false>"), 1)} {
		_, err = decode(bad)
		require.Error(t, err)
	}
}
func TestServiceUtilityAllowlist(t *testing.T) {
	for _, name := range []string{"/bin/sh", "/usr/bin/true", "launchctl", "/tmp/launchctl", "/bin/../bin/ps"} {
		t.Run(name, func(t *testing.T) {
			data, code, err := runCommand(t.Context(), name)
			require.EqualError(t, err, "unsupported service utility")
			require.Nil(t, data)
			require.Equal(t, -1, code)
		})
	}
}

func TestServiceOwnedUtilityBounds(t *testing.T) {
	data, code, err := runOwned(t.Context(), "/bin/sh", "-c", "printf literal")
	require.NoError(t, err)
	require.Zero(t, code)
	require.Equal(t, "literal", string(data))
	data, code, err = runOwned(t.Context(), "/bin/sh", "-c", "printf nonzero; exit 7")
	require.NoError(t, err)
	require.Equal(t, 7, code)
	require.Equal(t, "nonzero", string(data))
	// The leader exits while a descendant retains the output pipe. Cleanup
	// must fence the group, not mistake the zombie leader for an empty group.
	data, code, err = runOwned(t.Context(), "/bin/sh", "-c", "sleep 20 & printf descendant")
	require.NoError(t, err)
	require.Zero(t, code)
	require.Equal(t, "descendant", string(data))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, _, err = runOwned(ctx, "/bin/sh", "-c", "sleep 20 & wait")
	require.Error(t, err)
	_, _, err = runOwned(t.Context(), "/bin/sh", "-c", "head -c 1100000 /dev/zero")
	require.ErrorContains(t, err, "output exceeds bound")
}

func TestServiceInspectionReportsUtilityErrorWithoutOutputOrReplay(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	calls := 0
	f.m.run = func(_ context.Context, name string, args ...string) ([]byte, int, error) {
		calls++
		require.Equal(t, "/bin/launchctl", name)
		require.Equal(t, []string{"print", f.m.target()}, args)
		return []byte("UTILITY-OUTPUT-CANARY"), -1, fmt.Errorf("utility group cleanup failed: %w", syscall.EPERM)
	}
	result, err := f.m.execute(t.Context(), "restart", Changes{})
	require.ErrorIs(t, err, syscall.EPERM)
	require.ErrorContains(t, err, "launchd inspection unknown; no mutation is safe")
	require.ErrorContains(t, err, "utility group cleanup failed: operation not permitted")
	require.NotContains(t, err.Error(), "UTILITY-OUTPUT-CANARY")
	require.Equal(t, "unknown", result.Launchd)
	require.Equal(t, 1, calls)
	require.Empty(t, f.mutations)
	result, err = f.m.execute(t.Context(), "status", Changes{})
	require.NoError(t, err)
	require.Contains(t, result.Message, "utility group cleanup failed: operation not permitted")
	require.NotContains(t, result.Message, "UTILITY-OUTPUT-CANARY")
	require.True(t, result.Installed)
	require.Equal(t, "unknown", result.Launchd)
	require.Equal(t, 2, calls)
}

func TestServiceStopTimeoutDoesNotPublishOrStart(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	f.loaded = true
	f.running = true
	f.fail = "survive"
	before, err := os.ReadFile(f.m.plist())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	r, err := f.m.execute(ctx, "update", Changes{LogLevel: ptr("debug")})
	require.Error(t, err)
	require.Equal(t, "unknown", r.Launchd)
	require.Equal(t, []string{"bootout"}, f.mutations)
	after, err := os.ReadFile(f.m.plist())
	require.NoError(t, err)
	require.Equal(t, before, after)
}
func TestServiceAdoptsOptionalOutputAndExample(t *testing.T) {
	example, err := os.ReadFile("../../examples/launchd/agent-gateway.plist")
	require.NoError(t, err)
	_, err = decode(example)
	require.NoError(t, err)
	f := newFixture(t)
	f.install(t)
	d, _, _, err := f.m.read()
	require.NoError(t, err)
	d.Output = "json"
	d.LogLevel = "info"
	d.argv = nil
	encoded, err := d.encode()
	require.NoError(t, err)
	_, err = publish(f.m.plist(), encoded, true)
	require.NoError(t, err)
	_, err = f.m.execute(t.Context(), "update", Changes{LogLevel: ptr("debug")})
	require.NoError(t, err)
	d, _, _, err = f.m.read()
	require.NoError(t, err)
	require.Equal(t, "json", d.Output)
	require.Equal(t, "debug", d.LogLevel)
}
func TestServiceInstallUnsafeLogIsUnchanged(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.m.prepareDirectories())
	path := filepath.Join(f.m.logs(), "stdout.log")
	require.NoError(t, os.WriteFile(path, []byte("preserve"), 0644))
	_, err := f.m.execute(t.Context(), "install", Changes{})
	require.Error(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(data))
	require.Empty(t, f.mutations)
	_, err = os.Lstat(f.m.plist())
	require.ErrorIs(t, err, os.ErrNotExist)
}
func TestServiceAtomicPublicationRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.plist")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0600))
	ok, err := publish(path, []byte("replacement"), false)
	require.False(t, ok)
	require.Error(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "original", string(data))
}
