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

const allowAllPathsWarning = "--allow-all-paths disables repository path isolation; sandboxed callers can request operations on any absolute git repository path visible to the host"

var rootCmd = &cobra.Command{
	Use:   "local-git-mcp [--allow-all-paths] ALLOWED_PATH...",
	Short: "Stdio MCP server for authenticated git remote operations",
	Args:  cobra.ArbitraryArgs,
	RunE: func(_ *cobra.Command, args []string) error {
		runner := exec.NewOSRunner()
		gitClient, err := git.NewClientWithTimeout(runner, args, allowAllPaths, gitTimeout)
		if err != nil {
			return err
		}
		warnIfAllowAllPaths(allowAllPaths)
		handler := tools.NewHandler(gitClient)
		srv := newMCPServer(handler)

		slog.Info("starting local-git-mcp stdio server")
		return mcpserver.ServeStdio(srv)
	},
	SilenceUsage: true,
}

func init() {
	rootCmd.Flags().BoolVar(&allowAllPaths, "allow-all-paths", false, "allow access to any absolute git repository path")
	rootCmd.Flags().DurationVar(&gitTimeout, "git-timeout", git.DefaultCommandTimeout, "maximum duration for each git command (0 disables timeout)")
}

func newMCPServer(handler *tools.Handler) *mcpserver.MCPServer {
	srv := mcpserver.NewMCPServer(
		"local-git-mcp",
		"0.1.0",
		mcpserver.WithStrictInputSchemaDefault(),
		mcpserver.WithInputSchemaValidation(),
	)
	for _, tool := range handler.Tools() {
		srv.AddTool(tool, handler.Handle)
	}
	return srv
}

func warnIfAllowAllPaths(allow bool) {
	if !allow {
		return
	}
	slog.Warn(allowAllPathsWarning, "flag", "--allow-all-paths")
}
