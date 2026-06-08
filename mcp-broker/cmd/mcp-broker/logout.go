package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

var logoutCmd = &cobra.Command{
	Use:   "logout <server>",
	Short: "Clear a backend server's cached OAuth credentials",
	Long: `Clear a backend server's cached OAuth token and dynamic client registration
from the OS keychain.

Use this when a backend's stored login goes stale — for example after the
upstream rotates its OAuth client registration and tool calls start failing
with authorization errors. After clearing, restart the broker (or reconnect)
and the next call to the backend triggers a fresh OAuth flow.

This is distinct from "token rotate", which regenerates the broker's own
inbound bearer token used by clients and the dashboard.`,
	Args: func(cmd *cobra.Command, args []string) error {
		switch len(args) {
		case 1:
			return nil
		case 0:
			return fmt.Errorf("missing server name\n\nusage: %s\nexample: mcp-broker logout atlassian", cmd.UseLine())
		default:
			return fmt.Errorf("expected exactly one server name, got %d\n\nusage: %s", len(args), cmd.UseLine())
		}
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load(configPath())
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		if _, ok := cfg.Servers[name]; !ok {
			return fmt.Errorf("unknown server %q: %s", name, knownServers(cfg))
		}

		clearedToken, clearedClient, err := server.ClearCredentials(name)
		if err != nil {
			return fmt.Errorf("clearing credentials for %q: %w", name, err)
		}

		out := cmd.OutOrStdout()
		if !clearedToken && !clearedClient {
			_, _ = fmt.Fprintf(out, "No cached credentials found for %q; nothing to clear.\n", name)
			return nil
		}
		_, _ = fmt.Fprintf(out, "Cleared cached credentials for %q.\n", name)
		_, _ = fmt.Fprintf(out, "Restart the broker (or reconnect) to re-authenticate on the next call.\n")
		return nil
	},
}

// knownServers renders a hint listing the configured server names, for use in
// an unknown-server error.
func knownServers(cfg config.Config) string {
	if len(cfg.Servers) == 0 {
		return "no servers are configured"
	}
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return "configured servers are " + strings.Join(names, ", ")
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}
