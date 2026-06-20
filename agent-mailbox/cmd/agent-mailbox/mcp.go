package main

import (
	"fmt"

	"github.com/averycrespi/agent-tools/agent-mailbox/internal/mailboxmcp"
	"github.com/averycrespi/agent-tools/agent-mailbox/internal/store"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the stdio MCP server",
	Args:  cobra.NoArgs,
	RunE:  runMCP,
}

func runMCP(_ *cobra.Command, _ []string) error {
	st, err := store.Open(activeDBPath())
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close() //nolint:errcheck

	handler := mailboxmcp.NewHandler(st)
	srv := mcpserver.NewMCPServer("agent-mailbox", "0.1.0")
	for _, tool := range handler.Tools() {
		srv.AddTool(tool, handler.Handle)
	}
	return mcpserver.ServeStdio(srv)
}
