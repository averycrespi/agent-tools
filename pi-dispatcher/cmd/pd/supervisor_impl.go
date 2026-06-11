package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/control"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/pi"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	sbsandbox "github.com/averycrespi/agent-tools/sandbox-manager/pkg/sandbox"
	"github.com/spf13/cobra"
)

var (
	supervisorTaskID     string
	supervisorPiArgv     string
	supervisorEnvNames   string
	newSupervisorSandbox = func() (pipedSandboxClient, error) { return sbsandbox.New() }
)

type pipedSandboxClient interface {
	StartPiped(workdir string, args ...string) (sbsandbox.Process, error)
}

func init() {
	supervisorCmd.Flags().StringVar(&supervisorTaskID, "task-id", "", "task ID")
	supervisorCmd.Flags().StringVar(&supervisorPiArgv, "pi-argv", "", "JSON-encoded Pi argv")
	supervisorCmd.Flags().StringVar(&supervisorEnvNames, "env-names", "", "JSON-encoded environment variable names")
	supervisorCmd.RunE = runSupervisor
}

func runSupervisor(cmd *cobra.Command, _ []string) error {
	if supervisorTaskID == "" {
		return fmt.Errorf("--task-id is required")
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	task, err := db.GetTask(cmd.Context(), supervisorTaskID)
	if err != nil {
		return taskLookupError(supervisorTaskID, err)
	}
	run, err := db.LatestRun(cmd.Context(), supervisorTaskID)
	if err != nil {
		return runLookupError(supervisorTaskID, err)
	}
	argv, err := decodePiArgv(supervisorPiArgv)
	if err != nil {
		return failSupervisorStartup(cmd.Context(), db, run, err)
	}
	if len(argv) == 0 {
		argv = []string{"pi", "--mode", "rpc"}
	}
	envNames, err := decodeEnvNames(supervisorEnvNames)
	if err != nil {
		return failSupervisorStartup(cmd.Context(), db, run, err)
	}
	argv, err = piCommandWithEnv(argv, envNames)
	if err != nil {
		return failSupervisorStartup(cmd.Context(), db, run, err)
	}
	stdoutLog, stderrLog, piEvents, err := openRunLogs(run)
	if err != nil {
		return failSupervisorStartup(cmd.Context(), db, run, err)
	}
	defer stdoutLog.Close() //nolint:errcheck
	defer stderrLog.Close() //nolint:errcheck
	defer piEvents.Close()  //nolint:errcheck
	sb, err := newSupervisorSandbox()
	if err != nil {
		return failSupervisorStartup(cmd.Context(), db, run, err)
	}
	proc, err := sb.StartPiped(task.WorktreePath, argv...)
	if err != nil {
		return failSupervisorStartup(cmd.Context(), db, run, err)
	}
	defer proc.Kill() //nolint:errcheck
	if err := db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusRunning); err != nil {
		return finishSupervisor(cmd.Context(), db, run, proc, &supervisorRunState{}, err, err.Error())
	}
	go io.Copy(stderrLog, proc.Stderr()) //nolint:errcheck

	client := pi.NewClient(proc.Stdin(), io.TeeReader(proc.Stdout(), stdoutLog))
	state := &supervisorRunState{queuesEmpty: true}
	server, err := control.Listen(run.ControlSocketPath)
	if err == nil {
		defer server.Close() //nolint:errcheck
		go server.Serve(func(req control.Request) control.Response {
			switch req.Operation {
			case control.OpSteer:
				return response(client.Steer(req.Message))
			case control.OpFollowUp:
				return response(client.FollowUp(req.Message))
			case control.OpStop:
				_ = db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusStopping)
				return applyStopRequest(state, client, proc, req, stopGrace)
			case control.OpPing:
				return control.Response{OK: true}
			default:
				return control.Response{OK: false, Error: "unknown operation"}
			}
		}) //nolint:errcheck
	}

	if err := client.Prompt(task.Prompt); err != nil {
		return finishSupervisor(cmd.Context(), db, run, proc, state, err, err.Error())
	}
	for {
		event, raw, err := client.Next()
		if len(raw) > 0 {
			_, _ = piEvents.Write(append(raw, '\n'))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return finishSupervisor(cmd.Context(), db, run, proc, state, err, err.Error())
		}
		if event.IsExtensionUIRequest() && event.IsBlockingExtensionUI() {
			_ = client.ExtensionUIResponse(event.ID, true, nil)
			continue
		}
		if event.Type == "response" && !event.Success && event.Command != "abort" {
			errMsg := event.Error
			if errMsg == "" {
				errMsg = fmt.Sprintf("Pi command %q failed", event.Command)
			}
			_ = client.Abort()
			return finishSupervisor(cmd.Context(), db, run, proc, state, errors.New(errMsg), errMsg)
		}
		if event.Type == "queue_update" {
			state.queuesEmpty = queueUpdateEmpty(raw)
		}
		if event.Type == "response" && event.Command == "get_state" {
			state.piSessionFile = event.SessionFile()
			if state.awaitingState {
				return finishSupervisor(cmd.Context(), db, run, proc, state, nil, "agent run completed")
			}
		}
		if event.Type == "agent_end" && state.queuesEmpty {
			state.awaitingState = true
			if err := client.GetState(); err != nil {
				return finishSupervisor(cmd.Context(), db, run, proc, state, err, err.Error())
			}
		}
	}
	return finishSupervisor(cmd.Context(), db, run, proc, state, nil, "Pi process exited")
}

const (
	// stopGrace is how long the supervisor waits for Pi to exit on its own
	// after sending Abort before force-killing the process. `pd stop --force`
	// skips the grace and kills immediately.
	stopGrace = 10 * time.Second
	// supervisorFinishGrace bounds proc.Wait() inside finishSupervisor so the
	// supervisor can't hang if Pi doesn't exit when its stdin is closed.
	supervisorFinishGrace = 15 * time.Second
)

type supervisorRunState struct {
	mu            sync.Mutex
	stopRequested bool
	queuesEmpty   bool
	awaitingState bool
	piSessionFile string
}

func (s *supervisorRunState) finalStatus(err error) store.TaskStatus {
	s.mu.Lock()
	stopRequested := s.stopRequested
	s.mu.Unlock()
	if stopRequested {
		return store.StatusStopped
	}
	if err != nil {
		return store.StatusFailed
	}
	return store.StatusSucceeded
}

type abortClient interface {
	Abort() error
}

type killProcess interface {
	Kill() error
}

func applyStopRequest(state *supervisorRunState, client abortClient, proc killProcess, req control.Request, grace time.Duration) control.Response {
	state.mu.Lock()
	state.stopRequested = true
	state.mu.Unlock()
	abortErr := client.Abort()
	if req.Force {
		scheduleForceKill(proc, 0)
	} else {
		scheduleForceKill(proc, grace)
	}
	return response(abortErr)
}

func scheduleForceKill(proc killProcess, grace time.Duration) {
	if grace <= 0 {
		_ = proc.Kill()
		return
	}
	time.AfterFunc(grace, func() { _ = proc.Kill() })
}

func finishSupervisor(ctx context.Context, db *store.Store, run store.Run, proc interface {
	Stdin() io.WriteCloser
	Wait() error
	Kill() error
}, state *supervisorRunState, err error, message string) error {
	_ = proc.Stdin().Close()
	killTimer := time.AfterFunc(supervisorFinishGrace, func() { _ = proc.Kill() })
	defer killTimer.Stop()
	waitErr := proc.Wait()
	if err == nil {
		err = waitErr
		if err != nil && message == "Pi process exited" {
			message = err.Error()
		}
	}
	status := state.finalStatus(err)
	if message == "" && err != nil {
		message = err.Error()
	}
	if message == "" {
		message = "agent run completed"
	}
	exitCode := 0
	errorMessage := ""
	if status == store.StatusFailed {
		exitCode = 1
		errorMessage = message
	}
	if completeErr := db.CompleteRun(ctx, run.TaskID, status, exitCode, errorMessage, state.piSessionFile); completeErr != nil {
		return completeErr
	}
	runPostTerminalCleanup(ctx, db, run.TaskID, status)
	return nil
}

func failSupervisorStartup(ctx context.Context, db *store.Store, run store.Run, err error) error {
	if completeErr := db.CompleteRun(ctx, run.TaskID, store.StatusFailed, 1, err.Error(), ""); completeErr != nil {
		return completeErr
	}
	runPostTerminalCleanup(ctx, db, run.TaskID, store.StatusFailed)
	return err
}

func openRunLogs(run store.Run) (*os.File, *os.File, *os.File, error) {
	if err := os.MkdirAll(filepathDir(run.StdoutLogPath), 0o750); err != nil {
		return nil, nil, nil, err
	}
	stdoutLog, err := os.OpenFile(run.StdoutLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec
	if err != nil {
		return nil, nil, nil, err
	}
	stderrLog, err := os.OpenFile(run.StderrLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec
	if err != nil {
		_ = stdoutLog.Close()
		return nil, nil, nil, err
	}
	piEvents, err := os.OpenFile(run.PiEventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec
	if err != nil {
		_ = stdoutLog.Close()
		_ = stderrLog.Close()
		return nil, nil, nil, err
	}
	return stdoutLog, stderrLog, piEvents, nil
}

func response(err error) control.Response {
	if err != nil {
		return control.Response{OK: false, Error: err.Error()}
	}
	return control.Response{OK: true}
}

func queueUpdateEmpty(raw []byte) bool {
	var payload struct {
		Steering []string `json:"steering"`
		FollowUp []string `json:"followUp"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return true
	}
	return len(payload.Steering) == 0 && len(payload.FollowUp) == 0
}

func piCommandWithEnv(argv []string, names []string) ([]string, error) {
	if len(names) == 0 {
		return argv, nil
	}
	out := []string{"env"}
	for _, name := range names {
		value, ok := os.LookupEnv(name)
		if !ok {
			return nil, fmt.Errorf("env var %s was not provided", name)
		}
		out = append(out, name+"="+value)
	}
	out = append(out, argv...)
	return out, nil
}

func encodeEnvNames(names []string) (string, error) {
	return encodeStringList(names, "env names")
}

func decodeEnvNames(encoded string) ([]string, error) {
	return decodeStringList(encoded, "env names")
}

func encodePiArgv(argv []string) (string, error) {
	return encodeStringList(argv, "pi argv")
}

func decodePiArgv(encoded string) ([]string, error) {
	return decodeStringList(encoded, "Pi argv")
}

func encodeStringList(values []string, label string) (string, error) {
	for _, value := range values {
		if strings.ContainsRune(value, '\x00') {
			return "", fmt.Errorf("%s contains NUL byte", label)
		}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeStringList(encoded string, label string) ([]string, error) {
	if encoded == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	return values, nil
}

func filepathDir(path string) string {
	idx := strings.LastIndex(path, string(os.PathSeparator))
	if idx <= 0 {
		return "."
	}
	return path[:idx]
}
