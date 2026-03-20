package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add <branch>",
	Short: "Add a workspace",
	Long: `Add a workspace (worktree + tmux window) for a branch.

Skips any steps which have already been completed.
Must be run from the main repository, not a worktree.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("could not get working directory: %w", err)
		}
		if err := svc.Add(cwd, args[0]); err != nil {
			return err
		}
		attach, _ := cmd.Flags().GetBool("attach")
		if attach {
			return svc.Attach(cwd, args[0])
		}
		return nil
	},
}

func init() {
	addCmd.Flags().BoolP("attach", "a", false, "attach to the workspace after creation")
	addCmd.ValidArgsFunction = completeBranches
	rootCmd.AddCommand(addCmd)
}
