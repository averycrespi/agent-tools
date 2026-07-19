package cmd

import (
	"context"
	"io"

	"github.com/spf13/cobra"
)

func NewWTSD(run func(context.Context) error) *cobra.Command {
	command := &cobra.Command{
		Use:           "wtsd",
		Short:         "Run worktree-sync reconciliation in the foreground",
		Long:          "Run worktree-sync reconciliation in the foreground until SIGINT or SIGTERM.\nConfiguration, state, and managed worktrees use the standard XDG locations.",
		Args:          exactArgs("wtsd does not accept positional arguments", 0),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, _ []string) error {
			return run(command.Context())
		},
	}
	return command
}

func ExecuteWTSD(ctx context.Context, run func(context.Context) error, stdout, stderr io.Writer, args []string) error {
	command := NewWTSD(run)
	command.SetContext(ctx)
	if stdout != nil {
		command.SetOut(stdout)
	}
	if stderr != nil {
		command.SetErr(stderr)
	}
	command.SetArgs(args)
	return command.Execute()
}
