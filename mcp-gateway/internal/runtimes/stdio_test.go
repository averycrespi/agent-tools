//go:build darwin || linux

package runtimes

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const stdioFixtureMarker = "MCP_GATEWAY_STDIO_FIXTURE"

type fixtureInspection struct {
	Arguments    []string `json:"arguments"`
	Directory    string   `json:"directory"`
	Environment  []string `json:"environment"`
	SafeValue    string   `json:"safe_value"`
	SecretHash   string   `json:"secret_hash"`
	PID          int      `json:"pid"`
	ProcessGroup int      `json:"process_group"`
}

func TestStdioFixtureProcess(t *testing.T) {
	if os.Getenv(stdioFixtureMarker) != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(90)
	}
	arguments := os.Args[separator+1:]
	switch arguments[0] {
	case "inspect":
		directory, _ := os.Getwd()
		environment := os.Environ()
		for index, entry := range environment {
			environment[index], _, _ = strings.Cut(entry, "=")
		}
		sort.Strings(environment)
		digest := sha256.Sum256([]byte(os.Getenv("RUNTIME_SECRET")))
		inspection := fixtureInspection{Arguments: arguments[1:], Directory: directory, Environment: environment, SafeValue: os.Getenv("SAFE_VALUE"), SecretHash: hex.EncodeToString(digest[:]), PID: os.Getpid(), ProcessGroup: fixtureProcessGroup()}
		_ = json.NewEncoder(os.Stdout).Encode(inspection)
	case "frame":
		size, _ := strconv.Atoi(arguments[1])
		_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("x", size))
	case "stderr":
		size, _ := strconv.Atoi(arguments[1])
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("d", size))
		_, _ = fmt.Fprintln(os.Stdout, `{}`)
	case "partial":
		_, _ = fmt.Fprint(os.Stdout, `{}`)
	case "cooperative":
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		_, _ = fmt.Fprintln(os.Stdout, `{}`)
		select {}
	case "exit":
		code, _ := strconv.Atoi(arguments[1])
		os.Exit(code)
	case "mcp":
		fallback := false
		if arguments[1] == "auto" {
			if _, err := os.Stat(arguments[2]); err != nil {
				fallback = true
				_ = os.WriteFile(arguments[2], []byte("probe"), 0o600)
			}
		}
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var request struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(scanner.Bytes(), &request)
			switch request.Method {
			case "server/discover":
				if fallback {
					_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"Method not found"}}`+"\n", request.ID)
				} else {
					_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`+"\n", request.ID)
				}
			case "initialize":
				_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`+"\n", request.ID)
			case "tools/list":
				_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"fixture","description":"selected runtime","inputSchema":{"type":"object"}}]}}`+"\n", request.ID)
			}
		}
	default:
		os.Exit(91)
	}
	os.Exit(0)
}

func TestStdioSupervisorReapsPostSpawnSetupFailure(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	supervisor := NewStdioSupervisor(nil)
	var process *os.Process
	supervisor.captureGroup = func(received *os.Process) (int, bool) {
		process = received
		return 0, false
	}
	defer func() {
		if process != nil {
			_ = process.Kill()
			_, _ = process.Wait()
		}
	}()

	_, err = supervisor.Start(context.Background(), StdioDefinition{
		RuntimeID: "post-spawn-failure", Executable: executable,
		Arguments:        []string{"-test.run=^TestStdioFixtureProcess$", "--", "ignore-term"},
		WorkingDirectory: t.TempDir(), Environment: map[string]string{stdioFixtureMarker: "1"},
	})

	assert.ErrorIs(t, err, ErrStdioStartFailed)
	require.NotNil(t, process)
	assert.Error(t, process.Signal(syscall.Signal(0)))
	assert.Equal(t, int64(0), supervisor.Status().InUse)
}

func TestStdioSupervisorExecutesExactPathWithCleanRuntimeEnvironment(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	directory := t.TempDir()
	sentinel := filepath.Join(directory, "shell-expanded")
	canary := "T8-STATIC-SECRET-CANARY"
	t.Setenv("PATH", "/hostile/path")
	t.Setenv("HTTP_PROXY", "http://parent.invalid")
	t.Setenv("LD_LIBRARY_PATH", "/parent/library")
	supervisor := NewStdioSupervisor(nil)
	runtime, err := supervisor.Start(context.Background(), StdioDefinition{
		RuntimeID: "runtime-inspect", Executable: executable,
		Arguments:         []string{"-test.run=TestStdioFixtureProcess", "--", "inspect", "$(touch " + sentinel + ")"},
		WorkingDirectory:  directory,
		Environment:       map[string]string{stdioFixtureMarker: "1", "SAFE_VALUE": "declared"},
		SecretEnvironment: map[string]string{"RUNTIME_SECRET": "slot"}, Secrets: map[string]string{"slot": canary},
	})
	require.NoError(t, err)
	frame := receiveFrame(t, runtime.Frames())
	var inspection fixtureInspection
	require.NoError(t, json.Unmarshal(frame, &inspection))
	assert.Equal(t, []string{"$(touch " + sentinel + ")"}, inspection.Arguments)
	assert.Equal(t, directory, inspection.Directory)
	assert.Equal(t, "declared", inspection.SafeValue)
	digest := sha256.Sum256([]byte(canary))
	assert.Equal(t, hex.EncodeToString(digest[:]), inspection.SecretHash)
	assert.ElementsMatch(t, []string{stdioFixtureMarker, "RUNTIME_SECRET", "SAFE_VALUE"}, inspection.Environment)
	assert.NotContains(t, string(frame), canary)
	_, err = os.Stat(sentinel)
	assert.ErrorIs(t, err, os.ErrNotExist)
	exit := receiveExit(t, runtime.Done())
	assert.Equal(t, contract.ReasonProcessExited, exit.Reason)
	assert.True(t, exit.Retryable)
	assert.Zero(t, supervisor.Status().InUse)
}

func TestStdioProtocolFrameAndDiagnosticsBounds(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	frameMaximum := int(limitByName("stdio_protocol_frame_bytes"))
	stderrMaximum := int(limitByName("stdio_stderr_bytes"))
	for _, test := range []struct {
		name       string
		mode       string
		size       int
		wantReason contract.PublicReason
		wantFrame  int
		wantStderr int
	}{
		{name: "frame_n", mode: "frame", size: frameMaximum, wantReason: contract.ReasonProcessExited, wantFrame: frameMaximum},
		{name: "frame_n_plus_one", mode: "frame", size: frameMaximum + 1, wantReason: contract.ReasonOutputLimit},
		{name: "stderr_n", mode: "stderr", size: stderrMaximum, wantReason: contract.ReasonProcessExited, wantFrame: 2, wantStderr: stderrMaximum},
		{name: "stderr_n_plus_one", mode: "stderr", size: stderrMaximum + 1, wantReason: contract.ReasonProcessExited, wantFrame: 2, wantStderr: stderrMaximum},
	} {
		t.Run(test.name, func(t *testing.T) {
			supervisor := NewStdioSupervisor(nil)
			runtime, startErr := supervisor.Start(context.Background(), fixtureDefinition(executable, test.mode, strconv.Itoa(test.size)))
			require.NoError(t, startErr)
			if test.wantFrame != 0 {
				assert.Len(t, receiveFrame(t, runtime.Frames()), test.wantFrame)
			}
			exit := receiveExit(t, runtime.Done())
			assert.Equal(t, test.wantReason, exit.Reason)
			runtime.mu.Lock()
			assert.Len(t, runtime.diagnostics, test.wantStderr)
			runtime.mu.Unlock()
		})
	}
}

func TestStdioOutputRateHasExactBurstAndRefill(t *testing.T) {
	now := time.Unix(0, 0)
	limiter := newByteRateLimiter(func() time.Time { return now })
	burst := int(limitByName("stdio_output_burst_bytes"))
	assert.True(t, limiter.Allow(burst))
	assert.False(t, limiter.Allow(1))
	now = now.Add(time.Second)
	assert.True(t, limiter.Allow(burst))
	assert.False(t, limiter.Allow(1))

	stderrLimiter := newByteRateLimiter(func() time.Time { return now })
	assert.True(t, stderrLimiter.Allow(burst))
	assert.False(t, stderrLimiter.Allow(1))
}

func TestStdioSupervisorRejectsInvalidDefinitionsAndSecretSetsBeforeSpawn(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	valid := StdioDefinition{RuntimeID: "runtime-valid", Executable: executable, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{"SECRET": "slot"}, Secrets: map[string]string{"slot": "value"}}
	for _, mutate := range []func(*StdioDefinition){
		func(value *StdioDefinition) { value.Executable = "relative" },
		func(value *StdioDefinition) { value.Arguments = make([]string, 65) },
		func(value *StdioDefinition) { value.Environment = map[string]string{"SECRET": "duplicate"} },
		func(value *StdioDefinition) { value.SecretEnvironment = map[string]string{"SECRET": "INVALID-SLOT"} },
	} {
		definition := valid
		mutate(&definition)
		_, startErr := NewStdioSupervisor(nil).Start(context.Background(), definition)
		assert.ErrorIs(t, startErr, ErrStdioInvalidDefinition)
	}
	missing := valid
	missing.Secrets = map[string]string{}
	_, err = NewStdioSupervisor(nil).Start(context.Background(), missing)
	assert.ErrorIs(t, err, ErrStdioInvalidSecrets)
	extra := valid
	extra.Secrets = map[string]string{"slot": "value", "extra": "value"}
	_, err = NewStdioSupervisor(nil).Start(context.Background(), extra)
	assert.ErrorIs(t, err, ErrStdioInvalidSecrets)
}

func TestStdioSupervisorRejectsPartialNDJSONWithoutExposingIt(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	runtime, err := NewStdioSupervisor(nil).Start(context.Background(), fixtureDefinition(executable, "partial"))
	require.NoError(t, err)
	exit := receiveExit(t, runtime.Done())
	assert.Equal(t, contract.ReasonProtocolInvalid, exit.Reason)
	_, ok := <-runtime.Frames()
	assert.False(t, ok)
}

func TestStdioSupervisorAdmitsThirtyTwoWithoutAWaiter(t *testing.T) {
	supervisor := NewStdioSupervisor(nil)
	runtimes := make([]*StdioRuntime, 0, 32)
	for index := range 32 {
		runtime, err := supervisor.Start(context.Background(), StdioDefinition{RuntimeID: fmt.Sprintf("runtime-%d", index), Executable: "/bin/cat", WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{}, Secrets: map[string]string{}})
		require.NoError(t, err)
		runtimes = append(runtimes, runtime)
	}
	assert.Equal(t, int64(32), supervisor.Status().InUse)
	assert.True(t, supervisor.Status().Saturated)
	_, err := supervisor.Start(context.Background(), StdioDefinition{RuntimeID: "runtime-overflow", Executable: "/bin/cat", WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{}, Secrets: map[string]string{}})
	assert.ErrorIs(t, err, ErrStdioResourceLimit)
	for _, runtime := range runtimes {
		runtime.StopNow()
	}
	for _, runtime := range runtimes {
		_ = receiveExit(t, runtime.Done())
	}
	assert.Zero(t, supervisor.Status().InUse)
}

func TestStdioStopUsesGracefulInputAndVerifiedProcessGroup(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	runtime, err := NewStdioSupervisor(nil).Start(context.Background(), fixtureDefinition(executable, "cooperative"))
	require.NoError(t, err)
	assert.True(t, runtime.Stop(context.Background()))
	assert.Zero(t, runtime.supervisor.Status().InUse)
}

func TestStdioStopForcesReapAfterExactGraceWindow(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	supervisor := NewStdioSupervisor(nil)
	var durations []time.Duration
	supervisor.after = func(duration time.Duration) <-chan time.Time {
		durations = append(durations, duration)
		if duration == 3*time.Second {
			ready := make(chan time.Time)
			close(ready)
			return ready
		}
		return time.After(duration)
	}
	runtime, err := supervisor.Start(context.Background(), fixtureDefinition(executable, "ignore-term"))
	require.NoError(t, err)
	assert.Equal(t, []byte(`{}`), receiveFrame(t, runtime.Frames()))
	actualSignal := supervisor.signalGroup
	var forcedSignals []bool
	supervisor.signalGroup = func(process *os.Process, group int, force bool) bool {
		forcedSignals = append(forcedSignals, force)
		sent := actualSignal(process, group, force)
		if sent && force {
			// Force the schedule where reaping wins before the forced wait begins.
			select {
			case <-runtime.finished:
			case <-time.After(5 * time.Second):
				t.Error("forced process was not reaped")
			}
		}
		return sent
	}
	assert.True(t, runtime.Stop(context.Background()))
	assert.Equal(t, []bool{false, true}, forcedSignals)
	assert.Equal(t, []time.Duration{3 * time.Second}, durations)
	assert.Zero(t, supervisor.Status().InUse)
}

func TestStdioStopRejectsChangedProcessGroupAndAllowsCleanupRetry(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	supervisor := NewStdioSupervisor(nil)
	runtime, err := supervisor.Start(context.Background(), fixtureDefinition(executable, "ignore-term"))
	require.NoError(t, err)
	assert.Equal(t, []byte(`{}`), receiveFrame(t, runtime.Frames()))
	actualSignal := supervisor.signalGroup
	supervisor.signalGroup = func(*os.Process, int, bool) bool { return false }
	assert.False(t, runtime.Stop(context.Background()))
	assert.Equal(t, int64(1), supervisor.Status().InUse)
	supervisor.signalGroup = actualSignal
	assert.True(t, runtime.Stop(context.Background()))
	assert.Zero(t, supervisor.Status().InUse)
}

func TestStdioSupervisorClassifiesExitWithoutRawDetails(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, code := range []string{"0", "23"} {
		runtime, startErr := NewStdioSupervisor(nil).Start(context.Background(), fixtureDefinition(executable, "exit", code))
		require.NoError(t, startErr)
		exit := receiveExit(t, runtime.Done())
		assert.Equal(t, StdioExit{Reason: contract.ReasonProcessExited, Retryable: true}, exit)
	}
}

func fixtureDefinition(executable, mode string, arguments ...string) StdioDefinition {
	return StdioDefinition{RuntimeID: "runtime-" + mode + "-" + strings.Join(arguments, "-"), Executable: executable, Arguments: append([]string{"-test.run=TestStdioFixtureProcess", "--", mode}, arguments...), WorkingDirectory: "/", Environment: map[string]string{stdioFixtureMarker: "1"}, SecretEnvironment: map[string]string{}, Secrets: map[string]string{}}
}

func receiveFrame(t *testing.T, frames <-chan []byte) []byte {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(5 * time.Second):
		t.Fatal("stdio frame was not received")
		return nil
	}
}

func receiveExit(t *testing.T, done <-chan StdioExit) StdioExit {
	t.Helper()
	select {
	case exit := <-done:
		return exit
	case <-time.After(5 * time.Second):
		t.Fatal("stdio exit was not received")
		return StdioExit{}
	}
}
