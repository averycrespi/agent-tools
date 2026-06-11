package main

import (
	"log/slog"
	"time"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/exec"
	"github.com/averycrespi/agent-tools/local-git-mcp/internal/git"
	"github.com/averycrespi/agent-tools/local-git-mcp/internal/tools"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

var (
	allowAllPaths bool
	gitTimeout    time.Duration
)

var rootCmd = &cobra.Command{
	Use:   "local-git-mcp [--allow-all-paths] ALLOWED_PATH...",
	Short: "Stdio MCP server for authenticated git remote operations",
	Args:  cobra.ArbitraryArgs,
	RunE: func(_ *cobra.Command, args []string) error {
		if allowAllPaths {
			slog.Warn("SECURITY WARNING: --allow-all-paths disables repo path sandboxing; only use in trusted host contexts")
		}
		runner := exec.NewTimeoutRunner(exec.NewOSRunner(), gitTimeout)
		gitClient, err := git.NewClient(runner, args, allowAllPaths)
		if err != nil {
			return err
		}
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

func init() {
	rootCmd.Flags().BoolVar(&allowAllPaths, "allow-all-paths", false, "allow access to any absolute git repository path")
	rootCmd.Flags().DurationVar(&gitTimeout, "timeout", 2*time.Minute, "Git command timeout")
}
