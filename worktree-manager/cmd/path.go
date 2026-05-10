package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var pathCmd = &cobra.Command{
	Use:   "path <branch>",
	Short: "Print the expected worktree path",
	Long: `Print the expected absolute worktree path for a branch without creating it.
Must be run from the main repository, not a worktree.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("could not get working directory: %w", err)
		}
		path, err := svc.Path(cwd, args[0])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
		return err
	},
}

func init() {
	pathCmd.ValidArgsFunction = completeBranches
	rootCmd.AddCommand(pathCmd)
}
