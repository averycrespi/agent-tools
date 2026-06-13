package store

import (
	"database/sql"
	"time"
)

func scanTaskSummary(row taskScanner) (TaskSummary, error) {
	var summary TaskSummary
	var created, updated string
	var taskStatus, cleanupPolicy, cleanupStatus string
	var createdByPD int
	var cleanupAttemptedAt, removedAt sql.NullString
	var runID sql.NullString
	var runTaskID sql.NullString
	var runAttempt sql.NullInt64
	var supervisorPID sql.NullInt64
	var piSessionFile sql.NullString
	var runStatus sql.NullString
	var started sql.NullString
	var ended sql.NullString
	var exitCode sql.NullInt64
	var errorMessage sql.NullString
	var agentOptionsJSON sql.NullString
	var piArgvJSON sql.NullString
	var envVarNamesJSON sql.NullString
	var maxDurationSeconds sql.NullInt64
	var controlSocketPath sql.NullString
	var stdoutLogPath sql.NullString
	var stderrLogPath sql.NullString
	var piEventsPath sql.NullString

	if err := row.Scan(
		&summary.Task.ID, &summary.Task.RepoPath, &summary.Task.RepoName, &summary.Task.Branch, &summary.Task.WorktreePath, &summary.Task.PromptSource, &summary.Task.Prompt, &summary.Task.PromptPreview, &taskStatus, &cleanupPolicy, &createdByPD, &cleanupStatus, &summary.Task.WorktreeCleanupError, &cleanupAttemptedAt, &removedAt, &created, &updated,
		&runID, &runTaskID, &runAttempt, &supervisorPID, &piSessionFile, &runStatus, &started, &ended, &exitCode, &errorMessage, &agentOptionsJSON, &piArgvJSON, &envVarNamesJSON, &maxDurationSeconds, &controlSocketPath, &stdoutLogPath, &stderrLogPath, &piEventsPath,
	); err != nil {
		return TaskSummary{}, err
	}
	summary.Task.Status = TaskStatus(taskStatus)
	populateTaskCleanup(&summary.Task, cleanupPolicy, createdByPD, cleanupStatus, cleanupAttemptedAt, removedAt)
	summary.Task.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	summary.Task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)

	if !runID.Valid {
		return summary, nil
	}
	run := Run{
		ID:                 runID.String,
		TaskID:             runTaskID.String,
		Attempt:            int(runAttempt.Int64),
		SupervisorPID:      int(supervisorPID.Int64),
		PiSessionFile:      piSessionFile.String,
		ExitCode:           exitCode,
		ErrorMessage:       errorMessage.String,
		AgentOptionsJSON:   agentOptionsJSON.String,
		PiArgvJSON:         piArgvJSON.String,
		EnvVarNamesJSON:    envVarNamesJSON.String,
		MaxDurationSeconds: maxDurationSeconds.Int64,
		ControlSocketPath:  controlSocketPath.String,
		StdoutLogPath:      stdoutLogPath.String,
		StderrLogPath:      stderrLogPath.String,
		PiEventsPath:       piEventsPath.String,
	}
	populateRunTimes(&run, runStatus.String, started.String, ended)
	summary.LatestRun = OptionalRun{Run: run, Valid: true}
	return summary, nil
}
