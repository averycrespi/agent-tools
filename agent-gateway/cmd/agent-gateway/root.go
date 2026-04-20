package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func newRootCmd() *cobra.Command {
	var cfgFile string

	root := &cobra.Command{
		Use:           "agent-gateway",
		Short:         "HTTP/HTTPS proxy gateway for AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&cfgFile, "config", "", fmt.Sprintf("config file (default %q)", paths.ConfigFile()))

	// configPath resolves the effective config path for subcommands, evaluating
	// paths.ConfigFile() lazily so that XDG_CONFIG_HOME changes (e.g. in tests)
	// are respected at call time rather than at flag registration time.
	configPath := func() string {
		if cfgFile != "" {
			return cfgFile
		}
		return paths.ConfigFile()
	}

	root.AddCommand(newAgentCmd(configPath))
	root.AddCommand(newCACmd())
	root.AddCommand(newConfigCmd(configPath))
	root.AddCommand(newRulesCmd())
	root.AddCommand(newSecretCmd())
	root.AddCommand(newServeCmd(configPath))
	root.AddCommand(newAdminTokenCmd())
	root.AddCommand(newMasterKeyCmd())

	return root
}
