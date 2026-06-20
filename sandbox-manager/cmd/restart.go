package cmd

import "github.com/spf13/cobra"

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the sandbox and apply mount config",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return svc.Restart()
	},
}

func init() {
	rootCmd.AddCommand(restartCmd)
}
