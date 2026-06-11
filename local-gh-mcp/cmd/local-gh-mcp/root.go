package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/exec"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/tools"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

var ghTimeout time.Duration

var rootCmd = &cobra.Command{
	Use:   "local-gh-mcp",
	Short: "Stdio MCP server for GitHub operations via the gh CLI",
	RunE: func(_ *cobra.Command, _ []string) error {
		runner := exec.NewTimeoutRunner(exec.NewOSRunner(), ghTimeout)
		ghClient := gh.NewClient(runner)

		// Fast-fail if gh CLI is not authenticated
		if err := ghClient.AuthStatus(context.Background()); err != nil {
			return fmt.Errorf("gh CLI is not authenticated — run 'gh auth login' first: %w", err)
		}

		handler := tools.NewHandler(ghClient)

		srv := mcpserver.NewMCPServer("local-gh-mcp", "0.1.0")
		for _, tool := range handler.Tools() {
			srv.AddTool(tool, handler.Handle)
		}

		slog.Info("starting local-gh-mcp stdio server")
		return mcpserver.ServeStdio(srv)
	},
	SilenceUsage: true,
}

func init() {
	rootCmd.Flags().DurationVar(&ghTimeout, "timeout", 2*time.Minute, "gh command timeout")
}
