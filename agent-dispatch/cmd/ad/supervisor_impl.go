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

	"github.com/averycrespi/agent-tools/agent-dispatch/internal/control"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/pi"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/store"
	sbsandbox "github.com/averycrespi/agent-tools/sandbox-manager/pkg/sandbox"
	"github.com/spf13/cobra"
)

var (
	supervisorTaskID     string
	supervisorPiArgv     string
	newSupervisorSandbox = func() (pipedSandboxClient, error) { return sbsandbox.New() }
)

type pipedSandboxClient interface {
	StartPiped(workdir string, args ...string) (sbsandbox.Process, error)
}

func init() {
	supervisorCmd.Flags().StringVar(&supervisorTaskID, "task-id", "", "task ID")
	supervisorCmd.Flags().StringVar(&supervisorPiArgv, "pi-argv", "", "NUL-separated Pi argv")
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
		return err
	}
	run, err := db.LatestRun(cmd.Context(), supervisorTaskID)
	if err != nil {
		return err
	}
	if err := db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusRunning); err != nil {
		return err
	}
	addEvent(db, run, "supervisor.started", "supervisor started")

	argv, err := decodePiArgv(supervisorPiArgv)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		argv = []string{"pi", "--mode", "rpc"}
	}
	sb, err := newSupervisorSandbox()
	if err != nil {
		addEvent(db, run, "supervisor.failed", err.Error())
		_ = db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusFailed)
		return err
	}
	proc, err := sb.StartPiped(task.WorktreePath, argv...)
	if err != nil {
		addEvent(db, run, "supervisor.failed", err.Error())
		_ = db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusFailed)
		return err
	}
	defer proc.Kill() //nolint:errcheck

	if err := os.MkdirAll(filepathDir(run.StdoutLogPath), 0o750); err != nil {
		return err
	}
	stdoutLog, _ := os.OpenFile(run.StdoutLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec,errcheck
	defer stdoutLog.Close()                                                                    //nolint:errcheck
	stderrLog, _ := os.OpenFile(run.StderrLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec,errcheck
	defer stderrLog.Close()                                                                    //nolint:errcheck
	piEvents, _ := os.OpenFile(run.PiEventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)   //nolint:gosec,errcheck
	defer piEvents.Close()                                                                     //nolint:errcheck
	go io.Copy(stderrLog, proc.Stderr())                                                       //nolint:errcheck

	client := pi.NewClient(proc.Stdin(), io.TeeReader(proc.Stdout(), stdoutLog))
	state := &supervisorRunState{queuesEmpty: true}
	server, err := control.Listen(run.ControlSocketPath)
	if err != nil {
		addEvent(db, run, "control.failed", err.Error())
	} else {
		defer server.Close() //nolint:errcheck
		go server.Serve(func(req control.Request) control.Response {
			switch req.Operation {
			case control.OpSteer:
				return response(client.Steer(req.Message))
			case control.OpFollowUp:
				return response(client.FollowUp(req.Message))
			case control.OpStop:
				_ = db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusStopping)
				return applyStopRequest(state, client, proc, req, forceStopGrace)
			default:
				return control.Response{OK: false, Error: "unknown operation"}
			}
		}) //nolint:errcheck
	}

	if err := client.Prompt(task.Prompt); err != nil {
		addEvent(db, run, "supervisor.failed", err.Error())
		return db.CompleteRun(cmd.Context(), supervisorTaskID, store.StatusFailed, 1, err.Error(), "")
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
			addEvent(db, run, "pi.error", err.Error())
			return finishSupervisor(cmd.Context(), db, run, proc, state, err, err.Error())
		}
		if event.IsExtensionUIRequest() && event.IsBlockingExtensionUI() {
			_ = client.ExtensionUIResponse(event.ID, true, nil)
			addEvent(db, run, "pi.extension_ui.auto_cancelled", event.Method)
			continue
		}
		addPiEvent(db, run, event)
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

const forceStopGrace = 5 * time.Second

type supervisorRunState struct {
	mu             sync.Mutex
	stopRequested  bool
	forceRequested bool
	queuesEmpty    bool
	awaitingState  bool
	piSessionFile  string
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
	if err := client.Abort(); err != nil {
		return response(err)
	}
	state.mu.Lock()
	state.stopRequested = true
	state.forceRequested = req.Force
	state.mu.Unlock()
	if req.Force {
		scheduleForceKill(proc, grace)
	}
	return response(nil)
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
}, state *supervisorRunState, err error, message string) error {
	_ = proc.Stdin().Close()
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
	eventType := "supervisor.succeeded"
	exitCode := 0
	errorMessage := ""
	switch status {
	case store.StatusStopped:
		eventType = "supervisor.stopped"
	case store.StatusFailed:
		eventType = "supervisor.failed"
		exitCode = 1
		errorMessage = message
	}
	addEvent(db, run, eventType, message)
	return db.CompleteRun(ctx, run.TaskID, status, exitCode, errorMessage, state.piSessionFile)
}

func response(err error) control.Response {
	if err != nil {
		return control.Response{OK: false, Error: err.Error()}
	}
	return control.Response{OK: true}
}

func addPiEvent(db *store.Store, run store.Run, event pi.Event) {
	addEvent(db, run, event.CompactType(), event.Type)
}

func addEvent(db *store.Store, run store.Run, typ, message string) {
	_ = db.AddEvent(contextTODO(), store.Event{TaskID: run.TaskID, RunID: run.ID, Timestamp: time.Now(), Type: typ, Message: message})
}

func contextTODO() context.Context { return context.Background() }

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

func encodePiArgv(argv []string) (string, error) {
	for _, arg := range argv {
		if strings.ContainsRune(arg, '\x00') {
			return "", fmt.Errorf("pi argv contains NUL byte")
		}
	}
	data, err := json.Marshal(argv)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodePiArgv(encoded string) ([]string, error) {
	if encoded == "" {
		return nil, nil
	}
	var argv []string
	if err := json.Unmarshal([]byte(encoded), &argv); err != nil {
		return nil, fmt.Errorf("parse Pi argv: %w", err)
	}
	return argv, nil
}

func filepathDir(path string) string {
	idx := strings.LastIndex(path, string(os.PathSeparator))
	if idx <= 0 {
		return "."
	}
	return path[:idx]
}
