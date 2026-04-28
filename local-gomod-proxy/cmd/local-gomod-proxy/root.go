package main

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:          "local-gomod-proxy",
	Short:        "Host-side Go module proxy for sandboxed agents",
	SilenceUsage: true,
}
