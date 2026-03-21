package cmd

import "github.com/spf13/cobra"

var shellCmd = &cobra.Command{
	Use:                "shell [-- command]",
	Short:              "Open a shell in the sandbox",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Strip leading "--" if present.
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) == 0 {
			return svc.Shell()
		}
		return svc.Shell(args...)
	},
}

func init() {
	rootCmd.AddCommand(shellCmd)
}
