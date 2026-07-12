package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/app"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/dashboard"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/detect"
	analyzermcp "github.com/averycrespi/agent-tools/pi-session-analyzer/internal/mcp"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

type options struct{ sessionsDir, dbPath string }

func newRootCommand() *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{Use: "pi-session-analyzer", Short: "Analyze local Pi session logs", SilenceErrors: true, SilenceUsage: true}
	cmd.PersistentFlags().StringVar(&opts.sessionsDir, "sessions-dir", defaultSessionsDir(), "Pi JSONL sessions directory")
	cmd.PersistentFlags().StringVar(&opts.dbPath, "db", defaultDBPath(), "private analyzer SQLite database")
	cmd.AddCommand(newIngestCommand(opts), newListCommand(opts), newSummaryCommand(opts), newDetectCommand(opts), newMCPCommand(opts), newDashboardCommand(opts))
	return cmd
}

func defaultSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".pi", "agent", "sessions")
	}
	return filepath.Join(home, ".pi", "agent", "sessions")
}
func defaultDBPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".local", "share", "pi-session-analyzer", "sessions.db")
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "pi-session-analyzer", "sessions.db")
}

func newIngestCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "ingest", Short: "Ingest new or changed Pi sessions", Args: noArgs("ingest"), RunE: func(cmd *cobra.Command, _ []string) error {
		db, err := store.Open(opts.dbPath)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		result, err := app.New(db, detect.Registry()).Ingest(cmd.Context(), opts.sessionsDir)
		if encodeErr := writeJSON(cmd, result); encodeErr != nil {
			return encodeErr
		}
		return err
	}}
}
func newListCommand(opts *options) *cobra.Command {
	var limit int
	var cwd string
	cmd := &cobra.Command{Use: "list-sessions", Short: "List indexed sessions", Args: noArgs("list-sessions"), RunE: func(cmd *cobra.Command, _ []string) error {
		db, err := store.Open(opts.dbPath)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		rows, err := db.ListSessions(cmd.Context(), limit, cwd)
		if err != nil {
			return err
		}
		return writeJSON(cmd, rows)
	}}
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum sessions (1-100)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "filter by working directory")
	return cmd
}
func newSummaryCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "session-summary SESSION_ID", Short: "Summarize one indexed session", Args: exactlyOne("session-summary requires SESSION_ID; usage: pi-session-analyzer session-summary SESSION_ID"), RunE: func(cmd *cobra.Command, args []string) error {
		db, err := store.Open(opts.dbPath)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		summary, err := db.SessionSummary(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd, summary)
	}}
}
func newDetectCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "detect [SESSION_ID]", Short: "Recompute findings for all sessions or one session", Args: func(_ *cobra.Command, args []string) error {
		if len(args) > 1 {
			return fmt.Errorf("detect accepts at most one SESSION_ID; usage: pi-session-analyzer detect [SESSION_ID]")
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		db, err := store.Open(opts.dbPath)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		prefix := ""
		if len(args) == 1 {
			prefix = args[0]
		}
		err = app.New(db, detect.Registry()).Detect(cmd.Context(), prefix)
		if writeErr := writeJSON(cmd, map[string]string{"status": "complete"}); writeErr != nil {
			return writeErr
		}
		return err
	}}
}
func newMCPCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "mcp", Short: "Serve bounded read-only MCP tools over stdio", Args: noArgs("mcp"), RunE: func(cmd *cobra.Command, _ []string) error {
		boundary, err := robound.Open(cmd.Context(), opts.dbPath)
		if err != nil {
			return err
		}
		defer func() { _ = boundary.Close() }()
		handler := analyzermcp.NewHandler(boundary)
		server := mcpserver.NewMCPServer("pi-session-analyzer", "0.1.0")
		for _, tool := range handler.Tools() {
			server.AddTool(tool, handler.Handle)
		}
		return mcpserver.ServeStdio(server)
	}}
}

func newDashboardCommand(opts *options) *cobra.Command {
	var port int
	var noOpen bool
	cmd := &cobra.Command{Use: "dashboard", Short: "Serve the private loopback-only visual dashboard", Args: noArgs("dashboard"), RunE: func(cmd *cobra.Command, _ []string) error {
		return dashboard.Run(cmd.Context(), opts.dbPath, dashboard.Options{Port: port, NoOpen: noOpen, Output: cmd.OutOrStdout()})
	}}
	cmd.Flags().IntVar(&port, "port", 0, "loopback port (omitted chooses an ephemeral port)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open the default browser")
	return cmd
}

func noArgs(name string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fmt.Errorf("%s accepts no arguments; usage: pi-session-analyzer %s", name, name)
		}
		return nil
	}
}

func exactlyOne(message string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("%s", message)
		}
		return nil
	}
}
func writeJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
