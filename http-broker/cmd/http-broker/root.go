package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/http-broker/internal/paths"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:           "http-broker",
	Short:         "MITM forward proxy that injects credentials for sandboxed agents",
	SilenceUsage:  true,
	SilenceErrors: false,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", fmt.Sprintf("config file (default %q)", paths.ConfigFile()))
}

// configPath resolves the effective config file path, honouring --config.
func configPath() string {
	if cfgFile != "" {
		return cfgFile
	}
	return paths.ConfigFile()
}
