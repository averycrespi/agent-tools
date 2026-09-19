package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/averycrespi/agent-tools/typesafe-mcp/internal/provider"
	"github.com/averycrespi/agent-tools/typesafe-mcp/internal/server"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:          "typesafe-mcp",
	Short:        "Stateless TypeSafe inference over MCP stdio",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := provider.New(os.Getenv("TYPESAFE_API_KEY"))
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return server.Serve(ctx, client, os.Stdin, os.Stdout)
	},
}
