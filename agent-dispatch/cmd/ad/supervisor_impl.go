package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-dispatch/internal/control"
	adexec "github.com/averycrespi/agent-tools/agent-dispatch/internal/exec"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/pi"
	adsandbox "github.com/averycrespi/agent-tools/agent-dispatch/internal/sandbox"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/store"
	"github.com/spf13/cobra"
)

var (
	supervisorTaskID string
	supervisorPiArgv string
)

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

	argv := splitNUL(supervisorPiArgv)
	if len(argv) == 0 {
		argv = []string{"pi", "--mode", "rpc"}
	}
	proc, err := adsandbox.NewClient(adexec.NewOSRunner()).StartPiped(task.WorktreePath, argv...)
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
				return response(client.Abort())
			default:
				return control.Response{OK: false, Error: "unknown operation"}
			}
		}) //nolint:errcheck
	}

	if err := client.Prompt(task.Prompt); err != nil {
		addEvent(db, run, "supervisor.failed", err.Error())
		_ = db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusFailed)
		return err
	}
	queuesEmpty := true
	for {
		event, raw, err := client.Next()
		if len(raw) > 0 {
			_, _ = piEvents.Write(append(raw, '\n'))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			addEvent(db, run, "pi.error", err.Error())
			_ = db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusFailed)
			return err
		}
		if event.IsExtensionUIRequest() && event.IsBlockingExtensionUI() {
			_ = client.ExtensionUIResponse(event.ID, true, nil)
			addEvent(db, run, "pi.extension_ui.auto_cancelled", event.Method)
			continue
		}
		addPiEvent(db, run, event)
		if event.Type == "queue_update" {
			queuesEmpty = queueUpdateEmpty(raw)
		}
		if event.Type == "agent_end" && queuesEmpty {
			_ = client.GetState()
			_ = proc.Stdin().Close()
			_ = proc.Wait()
			addEvent(db, run, "supervisor.succeeded", "agent run completed")
			return db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusSucceeded)
		}
	}
	if err := proc.Wait(); err != nil {
		addEvent(db, run, "supervisor.failed", err.Error())
		return db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusFailed)
	}
	addEvent(db, run, "supervisor.succeeded", "Pi process exited")
	return db.UpdateStatuses(cmd.Context(), supervisorTaskID, store.StatusSucceeded)
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

func splitNUL(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

func filepathDir(path string) string {
	idx := strings.LastIndex(path, string(os.PathSeparator))
	if idx <= 0 {
		return "."
	}
	return path[:idx]
}
