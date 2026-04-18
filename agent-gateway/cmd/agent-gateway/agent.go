package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/config"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
)

// openAgentRegistry opens a short-lived agent registry using the state DB.
// Callers must invoke the returned cleanup function when done.
func openAgentRegistry() (agents.Registry, func(), error) {
	db, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, nil, fmt.Errorf("open state db: %w", err)
	}
	r, err := agents.NewRegistry(context.Background(), db)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("create agent registry: %w", err)
	}
	return r, func() { _ = db.Close() }, nil
}

// proxyListenAddr loads the proxy listen address from the config file at
// configPath. Falls back to the compiled-in default if the file cannot be
// loaded.
func proxyListenAddr(configPath string) string {
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Warn("could not load config; using default proxy listen address", "err", err)
		return config.DefaultConfig().Proxy.Listen
	}
	return cfg.Proxy.Listen
}

// proxyURL builds the ready-to-paste basic-auth proxy URL.
// The username "x" is a conventional placeholder; authentication relies on
// the token (password).
func proxyURL(token, listenAddr string) string {
	return "http://x:" + token + "@" + listenAddr
}

// printTokenBlock writes the token + ready-to-paste URL block to out.
func printTokenBlock(out io.Writer, token, listenAddr string) {
	url := proxyURL(token, listenAddr)
	_, _ = fmt.Fprintf(out, "token: %s\n", token)
	_, _ = fmt.Fprintf(out, "HTTPS_PROXY=%s\n", url)
	_, _ = fmt.Fprintf(out, "HTTP_PROXY=%s\n", url)
}

// newAgentCmd builds the "agent" command tree.
func newAgentCmd(configPath func() string) *cobra.Command {
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage registered agents",
	}

	agentCmd.AddCommand(newAgentAddCmd(configPath))
	agentCmd.AddCommand(newAgentListCmd())
	agentCmd.AddCommand(newAgentShowCmd())
	agentCmd.AddCommand(newAgentRotateCmd(configPath))
	agentCmd.AddCommand(newAgentRmCmd())

	return agentCmd
}

// newAgentAddCmd returns the "agent add <name>" command.
func newAgentAddCmd(configPath func() string) *cobra.Command {
	var desc string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a new agent and print its token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, cleanup, err := openAgentRegistry()
			if err != nil {
				return err
			}
			defer cleanup()

			listenAddr := proxyListenAddr(configPath())
			return execAgentAdd(
				cmd.Context(),
				r,
				args[0],
				desc,
				listenAddr,
				cmd.OutOrStdout(),
				func(pidPath string) error { return sendHUP(pidPath) },
			)
		},
	}
	cmd.Flags().StringVar(&desc, "desc", "", "human-readable description")
	return cmd
}

// execAgentAdd implements "agent add". Separated for testability.
func execAgentAdd(
	ctx context.Context,
	r agents.Registry,
	name, desc, listenAddr string,
	out io.Writer,
	signalFn func(string) error,
) error {
	tok, err := r.Add(ctx, name, desc)
	if err != nil {
		return fmt.Errorf("agent add: %w", err)
	}
	printTokenBlock(out, tok, listenAddr)
	_ = signalFn(paths.PIDFile())
	return nil
}

// newAgentListCmd returns the "agent list" command.
func newAgentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered agents (metadata only, no token)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, cleanup, err := openAgentRegistry()
			if err != nil {
				return err
			}
			defer cleanup()
			return execAgentList(cmd.Context(), r, cmd.OutOrStdout())
		},
	}
}

// execAgentList implements "agent list". Separated for testability.
func execAgentList(ctx context.Context, r agents.Registry, out io.Writer) error {
	metas, err := r.List(ctx)
	if err != nil {
		return fmt.Errorf("agent list: %w", err)
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tPREFIX\tCREATED\tLAST_SEEN\tDESCRIPTION")
	for _, m := range metas {
		lastSeen := "-"
		if m.LastSeenAt != nil {
			lastSeen = m.LastSeenAt.UTC().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			m.Name,
			m.TokenPrefix,
			m.CreatedAt.UTC().Format(time.RFC3339),
			lastSeen,
			m.Description,
		)
	}
	return w.Flush()
}

// newAgentShowCmd returns the "agent show <name>" command.
func newAgentShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show agent metadata (no token, no prefix)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, cleanup, err := openAgentRegistry()
			if err != nil {
				return err
			}
			defer cleanup()
			return execAgentShow(cmd.Context(), r, args[0], cmd.OutOrStdout())
		},
	}
}

// execAgentShow implements "agent show". Separated for testability.
// Prints metadata only — no token, not even the prefix.
func execAgentShow(ctx context.Context, r agents.Registry, name string, out io.Writer) error {
	metas, err := r.List(ctx)
	if err != nil {
		return fmt.Errorf("agent show: %w", err)
	}

	for _, m := range metas {
		if m.Name != name {
			continue
		}
		lastSeen := "-"
		if m.LastSeenAt != nil {
			lastSeen = m.LastSeenAt.UTC().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(out, "name:        %s\n", m.Name)
		_, _ = fmt.Fprintf(out, "description: %s\n", m.Description)
		_, _ = fmt.Fprintf(out, "created:     %s\n", m.CreatedAt.UTC().Format(time.RFC3339))
		_, _ = fmt.Fprintf(out, "last_seen:   %s\n", lastSeen)
		return nil
	}

	return agents.ErrNotFound
}

// newAgentRotateCmd returns the "agent rotate <name>" command.
func newAgentRotateCmd(configPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "rotate <name>",
		Short: "Mint a new token for an agent, invalidating the previous one",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, cleanup, err := openAgentRegistry()
			if err != nil {
				return err
			}
			defer cleanup()

			listenAddr := proxyListenAddr(configPath())
			return execAgentRotate(
				cmd.Context(),
				r,
				args[0],
				listenAddr,
				cmd.OutOrStdout(),
				func(pidPath string) error { return sendHUP(pidPath) },
			)
		},
	}
}

// execAgentRotate implements "agent rotate". Separated for testability.
func execAgentRotate(
	ctx context.Context,
	r agents.Registry,
	name, listenAddr string,
	out io.Writer,
	signalFn func(string) error,
) error {
	newTok, err := r.Rotate(ctx, name)
	if err != nil {
		if errors.Is(err, agents.ErrNotFound) {
			return err
		}
		return fmt.Errorf("agent rotate: %w", err)
	}
	printTokenBlock(out, newTok, listenAddr)
	_ = signalFn(paths.PIDFile())
	return nil
}

// newAgentRmCmd returns the "agent rm <name>" command.
func newAgentRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove an agent and cascade-delete its scoped secrets",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, cleanup, err := openAgentRegistry()
			if err != nil {
				return err
			}
			defer cleanup()

			return execAgentRm(
				cmd.Context(),
				r,
				args[0],
				cmd.OutOrStdout(),
				func(pidPath string) error { return sendHUP(pidPath) },
			)
		},
	}
}

// execAgentRm implements "agent rm". Separated for testability.
func execAgentRm(
	ctx context.Context,
	r agents.Registry,
	name string,
	out io.Writer,
	signalFn func(string) error,
) error {
	if err := r.Rm(ctx, name); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "removed: %s\n", name)
	_ = signalFn(paths.PIDFile())
	return nil
}
