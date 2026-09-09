//go:build e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestDemoHelper is a fixture entry point; the lifecycle owner supplies its
// arguments explicitly. Fault injection never enters the shipped executable.
func TestDemoHelper(t *testing.T) {
	index := 0
	for i, arg := range os.Args {
		if arg == "--" {
			index = i + 1
			break
		}
	}
	if index == 0 {
		t.Skip("subprocess fixture only")
	}
	args := os.Args[index:]
	if len(args) == 1 && args[0] == "--supervisor" {
		checkSupervisor(t)
		return
	}
	scenario := os.Getenv("DEMO_TEST_SCENARIO")
	opts := defaultOptions()
	opts.fixturePrefix = []string{"-test.run=^TestDemoHelper$", "--"}
	if scenario == "cleanup" {
		opts.remove = func(root string) error {
			secret, _ := readBearer(filepath.Join(root, "admin-bearer"))
			return errors.New(secret)
		}
	}
	if scenario == "gateway" || scenario == "fixture" {
		opts.start = func(label string, argv, env []string) (*child, error) {
			if (scenario == "gateway" && label == "Gateway") || (scenario == "fixture" && label == "workshop fixture") {
				argv = []string{"/usr/bin/false"}
			}
			return startChild(label, argv, env)
		}
	}
	if scenario == "timeout" {
		opts.startupTimeout = 200 * time.Millisecond
	}
	opts.configureClient = func(c *client) {
		original := c.http.Transport
		until := time.Now().Add(2 * time.Second)
		c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/readyz" && (scenario == "timeout" || (scenario == "delay" && time.Now().Before(until))) {
				return nil, errors.New("test readiness delay")
			}
			if scenario == "seed" && r.Method == "POST" && r.URL.Path == "/api/v1/principals" {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				_ = r.Body.Close()
				var body object
				if err = json.Unmarshal(raw, &body); err != nil {
					return nil, err
				}
				body["unexpected"] = true
				raw, err = json.Marshal(body)
				if err != nil {
					return nil, err
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
				r.ContentLength = int64(len(raw))
			}
			return original.RoundTrip(r)
		})
	}
	os.Exit(entry(args, os.Stdout, os.Stderr, opts))
}

type demoSubject struct {
	process        *testutil.RunningProcess
	parent, listen string
}
type readyManifest struct {
	Dataset   string            `json:"dataset"`
	Listen    string            `json:"listen"`
	Processes map[string]int    `json:"processes"`
	Fixtures  map[string]string `json:"fixtures"`
}

func authorityForTest(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}
func startDemo(t *testing.T, dataset, scenario, listen string, extra ...string) *demoSubject {
	t.Helper()
	parent := t.TempDir()
	t.Setenv("TMPDIR", parent)
	t.Setenv("HOME", parent)
	t.Setenv("DEMO_TEST_SCENARIO", scenario)
	require.NoError(t, os.WriteFile(filepath.Join(parent, "default-installation-sentinel"), []byte("unchanged"), 0600))
	if listen == "" {
		listen = authorityForTest(t)
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(90*time.Second, outputLimit)
	require.NoError(t, err)
	args := []string{"-test.run=^TestDemoHelper$", "--", "--listen", listen, "--dataset", dataset}
	args = append(args, extra...)
	process, err := runner.Start(t.Context(), exe, args...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = process.Stop() })
	return &demoSubject{process: process, parent: parent, listen: listen}
}
func (s *demoSubject) ready(t *testing.T) (string, readyManifest) {
	t.Helper()
	var root string
	var manifest readyManifest
	require.Eventually(t, func() bool {
		roots, err := filepath.Glob(filepath.Join(s.parent, "mcp-gateway-demo-*"))
		if err != nil || len(roots) != 1 {
			return false
		}
		raw, err := os.ReadFile(filepath.Join(roots[0], "ready.json"))
		if err != nil || json.Unmarshal(raw, &manifest) != nil {
			return false
		}
		root = roots[0]
		return true
	}, 75*time.Second, 50*time.Millisecond)
	require.NoDirExists(t, filepath.Join(s.parent, "go"), "isolated account must not relocate the build dependency cache")
	return root, manifest
}
func (s *demoSubject) stopped(t *testing.T, retained bool) testutil.ProcessResult {
	t.Helper()
	result, err := s.process.Wait()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.NotZero(t, result.ExitCode)
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
	require.False(t, result.StdoutTruncated)
	require.False(t, result.StderrTruncated)
	roots, err := filepath.Glob(filepath.Join(s.parent, "mcp-gateway-demo-*"))
	require.NoError(t, err)
	if retained {
		require.Len(t, roots, 1)
	} else {
		require.Empty(t, roots)
	}
	data, err := os.ReadFile(filepath.Join(s.parent, "default-installation-sentinel"))
	require.NoError(t, err)
	require.Equal(t, "unchanged", string(data))
	conn, err := net.DialTimeout("tcp4", s.listen, 200*time.Millisecond)
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err, "demo listener survived")
	return result
}
func testClient(t *testing.T, listen, root string) *client {
	t.Helper()
	bearer, err := readBearer(filepath.Join(root, "admin-bearer"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	return newClient(ctx, listen, bearer)
}

func checkSupervisor(t *testing.T) {
	t.Helper()
	for _, script := range []string{"printf finished; exit 0", "sleep 60 & printf finished; exit 0", "printf failed; exit 7", "trap '' TERM; printf ready; while :; do :; done", "yes output"} {
		c, err := startChild("supervisor fixture", []string{"/bin/sh", "-c", script}, os.Environ())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, c.finish(context.Background(), 0, false)) })
		require.Eventually(t, func() bool { return len(c.stdout.bytes()) > 0 }, 2*time.Second, 10*time.Millisecond)
		switch {
		case strings.Contains(script, "finished"):
			require.Eventually(t, func() bool { exited, err := c.poll(); require.NoError(t, err); return exited }, 2*time.Second, 10*time.Millisecond)
			group, err := syscall.Getpgid(c.cmd.Process.Pid)
			require.NoError(t, err, "group owner must remain live after command exit")
			require.Equal(t, c.cmd.Process.Pid, group)
			require.NoError(t, c.finish(t.Context(), time.Second, true))
			require.Zero(t, c.code)
		case strings.Contains(script, "failed"):
			require.ErrorContains(t, c.finish(t.Context(), time.Second, true), "failed or timed out")
			require.Equal(t, 7, c.code)
		case script == "yes output":
			require.Eventually(t, c.stdout.exceeded, 2*time.Second, 10*time.Millisecond)
			require.ErrorContains(t, c.finish(t.Context(), 0, false), "exceeded output bound")
			require.Len(t, c.stdout.bytes(), outputLimit)
		default:
			require.ErrorContains(t, c.finish(t.Context(), 20*time.Millisecond, true), "failed or timed out")
			require.Equal(t, -1, c.code)
		}
		require.True(t, c.settled)
		require.ErrorContains(t, c.signal(syscall.SIGKILL), "process identity changed")
		require.ErrorIs(t, syscall.Kill(-c.cmd.Process.Pid, 0), syscall.ESRCH)
	}
	literal := "spaces ; $(printf injected) 'quotes' $HOME"
	c, err := startChild("literal arguments", []string{"/bin/sh", "-c", "printf '%s' \"$1\"", "fixture", literal}, os.Environ())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.finish(context.Background(), 0, false)) })
	require.NoError(t, c.finish(t.Context(), time.Second, true))
	require.Equal(t, literal, string(c.stdout.bytes()))
}

func TestServeDemoLifecycle(t *testing.T) {
	runner, err := testutil.NewBinaryRunner(15*time.Second, outputLimit)
	require.NoError(t, err)
	result, err := runner.Run(t.Context(), "go", "env", "-json", "GOCACHE", "GOMODCACHE")
	require.NoError(t, err)
	var caches map[string]string
	require.NoError(t, json.Unmarshal(result.Stdout, &caches))
	// Both caches otherwise follow the isolated HOME when CI leaves GOPATH unset.
	for _, name := range []string{"GOCACHE", "GOMODCACHE"} {
		require.True(t, filepath.IsAbs(caches[name]))
		t.Setenv(name, caches[name])
	}
	t.Setenv("GOPATH", "")
	t.Run("bootstrap from absent output directory", func(t *testing.T) {
		checkout := t.TempDir()
		module := defaultOptions().module
		sources, err := filepath.Glob(filepath.Join(module, "test", "demo", "*.go"))
		require.NoError(t, err)
		sources = append(sources, filepath.Join(module, "go.mod"), filepath.Join(module, "go.sum"), filepath.Join(module, "scripts", "serve-demo.sh"))
		for _, source := range sources {
			if strings.HasSuffix(source, "_test.go") {
				continue
			}
			relative, err := filepath.Rel(module, source)
			require.NoError(t, err)
			target := filepath.Join(checkout, relative)
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0700))
			data, err := os.ReadFile(source)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(target, data, 0600))
		}
		require.NoDirExists(t, filepath.Join(checkout, ".demo-bin"))
		t.Setenv("GOWORK", "off")
		runner, err := testutil.NewBinaryRunner(3*time.Minute, outputLimit)
		require.NoError(t, err)
		result, err := runner.Run(t.Context(), "bash", filepath.Join(checkout, "scripts", "serve-demo.sh"), "--help")
		require.NoError(t, err, "%s\n%s", result.Stdout, result.Stderr)
		require.Zero(t, result.ExitCode)
		require.Contains(t, string(result.Stdout), "serve-demo [--dataset curated|empty]")
		require.FileExists(t, filepath.Join(checkout, ".demo-bin", "serve-demo"))
		require.False(t, result.StdoutTruncated)
		require.False(t, result.StderrTruncated)
		require.True(t, result.Cleanup.Reaped)
		require.False(t, result.Cleanup.Survived)
	})
	t.Run("curated public results privacy and fixture exit", func(t *testing.T) {
		s := startDemo(t, "curated", "", "")
		root, manifest := s.ready(t)
		require.Equal(t, "curated", manifest.Dataset)
		require.Len(t, manifest.Fixtures, 2)
		c := testClient(t, s.listen, root)
		require.Len(t, rows(c.get("servers"), "items"), 2)
		require.Len(t, rows(c.get("grants"), "items"), 13)
		names := []string{}
		for _, item := range rows(c.get("principals"), "items") {
			row, _ := item.(map[string]any)
			names = append(names, text(row, "display_name"))
		}
		require.ElementsMatch(t, []string{"Demo Explorer", "Demo Reader", "Demo Disabled", "Demo Request Tool", "Demo Request Constraints", "Demo Request Duration", "Demo Request Server", "Demo Request Read-only"}, names)
		requests := rows(c.get("grant-requests"), "items")
		require.Len(t, requests, 5)
		for _, item := range requests {
			request, _ := item.(map[string]any)
			require.Equal(t, "pending", text(request, "state"))
		}
		explorer, err := readBearer(filepath.Join(root, "explorer-bearer"))
		require.NoError(t, err)
		reader, err := readBearer(filepath.Join(root, "reader-bearer"))
		require.NoError(t, err)
		discovered := map[string]bool{}
		for _, item := range rows(c.rpc(reader, "tools/list", object{}), "result", "tools") {
			row, _ := item.(map[string]any)
			discovered[text(row, "name")] = true
		}
		require.True(t, discovered["demo_workshop.add"])
		require.False(t, discovered["demo_workshop.controlled_error"])
		before := rows(c.get("invocations"), "items")
		time.Sleep(200 * time.Millisecond)
		require.Equal(t, before, rows(c.get("invocations"), "items"), "background activity")
		require.True(t, contentIs(c.call(explorer, "demo_workshop.add", object{"a": 40, "b": 2}), "42"))
		require.Len(t, rows(c.get("invocations"), "items"), len(before)+1)
		require.Equal(t, "call_rejected", text(c.call(reader, "demo_workshop.add", object{"a": 1, "b": 2}), "error", "data", "code"))
		require.Equal(t, "downstream_failure", text(c.call(explorer, "demo_workshop.controlled_error", object{}), "error", "data", "code"))
		require.True(t, contentIs(c.call(reader, "demo_library.lookup", object{"document": "welcome"}), documents["welcome"]))
		require.NoError(t, c.err)
		verifyDemoRequestApprovals(t, c, root)
		info, err := os.Stat(root)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0700), info.Mode().Perm())
		sinks, err := filepath.Glob(filepath.Join(root, "*-bearer"))
		require.NoError(t, err)
		require.Len(t, sinks, 9)
		secrets := [][]byte{}
		for _, sink := range sinks {
			info, err = os.Stat(sink)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0600), info.Mode().Perm())
			raw, err := os.ReadFile(sink)
			require.NoError(t, err)
			secret := bytes.TrimSpace(raw)
			secrets = append(secrets, secret)
			require.NoError(t, filepath.WalkDir(filepath.Join(root, "data"), func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if !entry.IsDir() {
					data, readErr := os.ReadFile(path)
					if readErr != nil {
						return readErr
					}
					require.NotContains(t, string(data), string(secret), "credential in durable state")
				}
				return nil
			}))
		}
		pid := manifest.Processes["workshop fixture"]
		pgid, err := syscall.Getpgid(pid)
		require.NoError(t, err)
		require.Equal(t, pid, pgid)
		require.NoError(t, syscall.Kill(-pid, syscall.SIGTERM))
		result := s.stopped(t, false)
		require.Contains(t, string(result.Stderr), "fixture exited unexpectedly")
		require.Contains(t, string(result.Stdout), "Demo Gateway ready")
		for _, secret := range secrets {
			require.NotContains(t, string(append(result.Stdout, result.Stderr...)), string(secret))
		}
		for _, port := range manifest.Fixtures {
			conn, err := net.DialTimeout("tcp4", "127.0.0.1:"+port, 200*time.Millisecond)
			if conn != nil {
				_ = conn.Close()
			}
			require.Error(t, err)
		}
	})
	t.Run("empty fresh runs and signals", func(t *testing.T) {
		roots, bearers := map[string]bool{}, map[string]bool{}
		for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
			s := startDemo(t, "empty", "", "")
			root, manifest := s.ready(t)
			require.Empty(t, manifest.Fixtures)
			require.Len(t, manifest.Processes, 1)
			require.Contains(t, manifest.Processes, "Gateway")
			roots[root] = true
			bearer, err := readBearer(filepath.Join(root, "admin-bearer"))
			require.NoError(t, err)
			bearers[bearer] = true
			c := testClient(t, s.listen, root)
			for _, collection := range []string{"servers", "principals", "grants", "grant-requests", "invocations"} {
				require.Empty(t, rows(c.get(collection), "items"))
			}
			require.NoError(t, c.err)
			require.NoError(t, s.process.Signal(sig))
			if sig == syscall.SIGHUP {
				require.NoError(t, s.process.Signal(sig))
			}
			s.stopped(t, false)
		}
		require.Len(t, roots, 3)
		require.Len(t, bearers, 3)
	})
	t.Run("invalid selectors and occupied listener", func(t *testing.T) {
		for _, args := range [][]string{{"--dataset", "unknown"}, {"--listen", "localhost:8211"}, {"--listen", "0.0.0.0:8211"}, {"--listen", "127.0.0.1:0"}, {"--listen", "127.0.0.1:08211"}, {"--listen", "127.0.0.1:8211;exit"}, {"--unknown"}} {
			s := startDemo(t, "empty", "", "", args...)
			result := s.stopped(t, false)
			require.Equal(t, 2, result.ExitCode)
			require.NotContains(t, string(result.Stdout), "Building")
		}
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { require.NoError(t, listener.Close()) }()
		s := startDemo(t, "empty", "", listener.Addr().String())
		result, err := s.process.Wait()
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		require.NotZero(t, result.ExitCode)
		require.Contains(t, string(result.Stderr), "listener unavailable")
		roots, err := filepath.Glob(filepath.Join(s.parent, "mcp-gateway-demo-*"))
		require.NoError(t, err)
		require.Empty(t, roots)
	})
	t.Run("delayed readiness and selected failures", func(t *testing.T) {
		began := time.Now()
		s := startDemo(t, "empty", "delay", "")
		s.ready(t)
		require.GreaterOrEqual(t, time.Since(began), 2*time.Second)
		require.NoError(t, s.process.Signal(syscall.SIGTERM))
		s.stopped(t, false)
		for _, scenario := range []string{"seed", "fixture", "gateway", "timeout"} {
			s := startDemo(t, "curated", scenario, "")
			result := s.stopped(t, false)
			require.NotContains(t, string(result.Stdout), "Demo Gateway ready")
		}
	})
	t.Run("listener reuse after connection close", func(t *testing.T) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		listen := listener.Addr().String()
		conn, err := net.DialTimeout("tcp4", listen, time.Second)
		require.NoError(t, err)
		accepted, err := listener.Accept()
		require.NoError(t, err)
		require.NoError(t, accepted.Close())
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		_, err = conn.Read(make([]byte, 1))
		require.ErrorIs(t, err, io.EOF)
		require.NoError(t, conn.Close())
		require.NoError(t, listener.Close())
		s := startDemo(t, "empty", "", listen)
		s.ready(t)
		require.NoError(t, s.process.Signal(syscall.SIGTERM))
		s.stopped(t, false)
	})
	t.Run("filesystem cleanup failure reports retained root", func(t *testing.T) {
		s := startDemo(t, "empty", "cleanup", "")
		root, manifest := s.ready(t)
		secret, err := readBearer(filepath.Join(root, "admin-bearer"))
		require.NoError(t, err)
		require.NoError(t, s.process.Signal(syscall.SIGTERM))
		result := s.stopped(t, true)
		require.FileExists(t, filepath.Join(root, "admin-bearer"))
		require.Contains(t, string(result.Stderr), "cleanup unconfirmed; retained "+root)
		require.NotContains(t, string(append(result.Stdout, result.Stderr...)), secret)
		for _, pid := range manifest.Processes {
			require.ErrorIs(t, syscall.Kill(-pid, 0), syscall.ESRCH)
		}
	})
	t.Run("live process-group ownership", func(t *testing.T) {
		exe, err := os.Executable()
		require.NoError(t, err)
		runner, err := testutil.NewBinaryRunner(30*time.Second, outputLimit)
		require.NoError(t, err)
		result, err := runner.Run(t.Context(), exe, "-test.run=^TestDemoHelper$", "--", "--supervisor")
		require.NoError(t, err, "%s\n%s", result.Stdout, result.Stderr)
		require.Zero(t, result.ExitCode)
		require.True(t, result.Cleanup.Reaped)
		require.False(t, result.Cleanup.Survived)
	})
	t.Run("fixture tools are bounded", func(t *testing.T) {
		result, err := toolResult("workshop", "add", object{"a": float64(-5), "b": float64(2)})
		require.NoError(t, err)
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"text":"-3"`)
		for _, test := range []struct {
			kind, name string
			args       object
		}{{"workshop", "echo", object{"text": strings.Repeat("x", 257)}}, {"workshop", "add", object{"a": math.Inf(1), "b": float64(1)}}, {"workshop", "add", object{"a": true, "b": float64(1)}}, {"workshop", "add", object{"a": "__import__('os')", "b": float64(1)}}, {"library", "lookup", object{"document": "/etc/passwd"}}, {"library", "echo", object{"text": "wrong server"}}} {
			_, err := toolResult(test.kind, test.name, test.args)
			require.Error(t, err)
		}
		for _, port := range []int{1, 8211, 65535} {
			require.True(t, validAuthority("127.0.0.1:"+strconv.Itoa(port)))
		}
	})
}
