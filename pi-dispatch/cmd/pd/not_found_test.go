package main

import (
	"context"
	"path/filepath"
	"testing"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestTaskCommandsReportHelpfulNotFoundErrors(t *testing.T) {
	setupEmptyTaskDB(t)

	tests := []struct {
		name string
		run  func(*cobra.Command) error
	}{
		{name: "status", run: func(cmd *cobra.Command) error { return showStatus(cmd, []string{"123"}) }},
		{name: "wait", run: func(cmd *cobra.Command) error { return waitForTask(waitTestCommand(t, 0), []string{"123"}) }},
		{name: "logs", run: func(cmd *cobra.Command) error { return showLogs(cmd, []string{"123"}) }},
		{name: "rm", run: func(cmd *cobra.Command) error { return removeTask(removeTestCommand(t, false), []string{"123"}) }},
		{name: "steer", run: func(cmd *cobra.Command) error { return sendSteer(cmd, []string{"123", "focus"}) }},
		{name: "followup", run: func(cmd *cobra.Command) error { return sendFollowUp(cmd, []string{"123", "next"}) }},
		{name: "stop", run: func(cmd *cobra.Command) error { return sendStop(stopCommand(t), []string{"123"}) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(testCommand())

			require.Error(t, err)
			require.Equal(t, "task 123 not found", err.Error())
			require.NotContains(t, err.Error(), "sql")
			require.NotContains(t, err.Error(), "no rows")
		})
	}
}

func setupEmptyTaskDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	t.Cleanup(func() { cfg = oldCfg })
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func testCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

func stopCommand(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := testCommand()
	cmd.Flags().Bool("force", false, "")
	return cmd
}
