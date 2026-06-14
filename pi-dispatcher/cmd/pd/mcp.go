package main

import (
	"fmt"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/pdmcp"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the read-only MCP server",
	Args:  cobra.NoArgs,
	RunE:  runMCP,
}

func runMCP(_ *cobra.Command, _ []string) error {
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close() //nolint:errcheck

	handler := pdmcp.NewHandler(st)
	srv := mcpserver.NewMCPServer("pd", "0.1.0")
	for _, tool := range handler.Tools() {
		srv.AddTool(tool, handler.Handle)
	}
	return mcpserver.ServeStdio(srv)
}
