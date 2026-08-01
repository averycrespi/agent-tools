package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/egress-broker/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect and refresh config.json",
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the effective config file path",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cmd.Println(configPath())
		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the effective configuration",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(configPath())
		if err != nil {
			return err
		}
		out, err := marshalIndent(cfg)
		if err != nil {
			return err
		}
		cmd.Println(string(out))
		return nil
	},
}

var configRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Backfill newly added defaults into config.json",
	Long: "Reads config.json, fills in any default that the file omits, and writes it back.\n" +
		"Existing values are preserved; only absent fields are added.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		written, err := config.Refresh(configPath())
		if err != nil {
			return err
		}
		cmd.Println(fmt.Sprintf("wrote %s", written))
		return nil
	},
}

func init() {
	configCmd.AddCommand(configPathCmd, configShowCmd, configRefreshCmd)
	rootCmd.AddCommand(configCmd)
}
