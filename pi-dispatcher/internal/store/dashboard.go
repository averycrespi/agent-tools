package store

import "context"

type OptionalRun struct {
	Run   Run
	Valid bool
}

type TaskSummary struct {
	Task      Task
	LatestRun OptionalRun
}

func (s *Store) ListTaskSummaries(ctx context.Context) ([]TaskSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    t.id, t.repo_path, t.repo_name, t.branch, t.worktree_path, t.prompt_source, t.prompt, t.prompt_preview, t.status, t.worktree_cleanup_policy, t.worktree_created_by_pd, t.worktree_cleanup_status, t.worktree_cleanup_error, t.worktree_cleanup_attempted_at, t.worktree_removed_at, t.created_at, t.updated_at,
    r.id, r.task_id, r.attempt, r.supervisor_pid, r.pi_session_file, r.status, r.started_at, r.ended_at, r.exit_code, r.error_message, r.agent_options_json, r.pi_argv_json, r.env_var_names_json, r.control_socket_path, r.stdout_log_path, r.stderr_log_path, r.pi_events_path
FROM tasks t
LEFT JOIN runs r ON r.id = (
    SELECT id FROM runs WHERE task_id = t.id ORDER BY attempt DESC LIMIT 1
)
ORDER BY t.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var summaries []TaskSummary
	for rows.Next() {
		summary, err := scanTaskSummary(rows)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}
