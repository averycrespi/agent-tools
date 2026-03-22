package main

import (
	"log/slog"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/exec"
	"github.com/averycrespi/agent-tools/local-git-mcp/internal/git"
	"github.com/averycrespi/agent-tools/local-git-mcp/internal/tools"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "local-git-mcp",
	Short: "Stdio MCP server for authenticated git remote operations",
	RunE: func(_ *cobra.Command, _ []string) error {
		runner := exec.NewOSRunner()
		gitClient := git.NewClient(runner)
		handler := tools.NewHandler(gitClient)

		srv := mcpserver.NewMCPServer("local-git-mcp", "0.1.0")
		for _, tool := range handler.Tools() {
			srv.AddTool(tool, handler.Handle)
		}

		slog.Info("starting local-git-mcp stdio server")
		return mcpserver.ServeStdio(srv)
	},
	SilenceUsage: true,
}
