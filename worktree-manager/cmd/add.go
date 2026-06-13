package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add <branch>",
	Short: "Add a worktree",
	Long: `Add a worktree with a tmux window for a branch.

Skips any steps which have already been completed.
Must be run from the main repository, not a worktree.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("could not get working directory: %w", err)
		}
		attach, _ := cmd.Flags().GetBool("attach")
		noWindow, _ := cmd.Flags().GetBool("no-window")
		reconfigure, _ := cmd.Flags().GetBool("reconfigure")
		if attach && noWindow {
			return fmt.Errorf("--attach cannot be used with --no-window")
		}
		if noWindow {
			if reconfigure {
				_, err := svc.AddHeadlessReconfigure(cwd, args[0])
				return err
			}
			_, err := svc.AddHeadless(cwd, args[0])
			return err
		}
		if reconfigure {
			if err := svc.AddReconfigure(cwd, args[0]); err != nil {
				return err
			}
		} else if err := svc.Add(cwd, args[0]); err != nil {
			return err
		}
		if attach {
			return svc.Attach(cwd, args[0])
		}
		return nil
	},
}

func init() {
	addCmd.Flags().BoolP("attach", "a", false, "attach to the worktree after creation")
	addCmd.Flags().Bool("no-window", false, "create/configure the worktree without creating a tmux window")
	addCmd.Flags().Bool("reconfigure", false, "rerun configured file copy and setup scripts for an existing worktree")
	addCmd.ValidArgsFunction = completeBranches
	rootCmd.AddCommand(addCmd)
}
