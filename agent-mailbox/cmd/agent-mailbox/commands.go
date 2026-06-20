package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/averycrespi/agent-tools/agent-mailbox/internal/store"
	"github.com/spf13/cobra"
)

var sendFlags struct {
	sender           string
	subject          string
	body             string
	channel          string
	threadID         string
	severity         string
	requiresResponse bool
	idempotencyKey   string
}

var listFlags struct {
	status           string
	channel          string
	sender           string
	severity         string
	requiresResponse bool
	hasRequires      bool
	limit            int
	offset           int
}

var (
	actor      string
	resolution string
)

var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a mailbox message",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		st, err := store.Open(activeDBPath())
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer st.Close() //nolint:errcheck
		msg, created, err := st.SendMessage(cmd.Context(), store.SendMessageParams{Sender: sendFlags.sender, Subject: sendFlags.subject, Body: sendFlags.body, Channel: sendFlags.channel, ThreadID: sendFlags.threadID, Severity: store.Severity(sendFlags.severity), RequiresResponse: sendFlags.requiresResponse, IdempotencyKey: sendFlags.idempotencyKey})
		if err != nil {
			return err
		}
		return printJSON(cmd, map[string]any{"message": msg, "created": created})
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List mailbox messages",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		st, err := store.Open(activeDBPath())
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer st.Close() //nolint:errcheck
		p := store.ListMessagesParams{Status: store.Status(listFlags.status), Channel: listFlags.channel, Sender: listFlags.sender, Severity: store.Severity(listFlags.severity), Limit: listFlags.limit, Offset: listFlags.offset}
		if listFlags.hasRequires {
			p.RequiresResponse = &listFlags.requiresResponse
		}
		result, err := st.ListMessages(cmd.Context(), p)
		if err != nil {
			return err
		}
		return printJSON(cmd, result)
	},
}

var readCmd = &cobra.Command{
	Use:   "read <id>",
	Short: "Read one mailbox message",
	Args:  requireOneArg("message id"),
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := store.Open(activeDBPath())
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer st.Close() //nolint:errcheck
		detail, err := st.GetMessage(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return printJSON(cmd, detail)
	},
}

var ackCmd = lifecycleCmd("ack", "Acknowledge a mailbox message", func(ctx context.Context, st *store.Store, id, actor string) (store.Message, bool, error) {
	return st.AckMessage(ctx, id, actor)
})

var resolveCmd = &cobra.Command{
	Use:   "resolve <id>",
	Short: "Resolve a mailbox message",
	Args:  requireOneArg("message id"),
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := store.Open(activeDBPath())
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer st.Close() //nolint:errcheck
		msg, changed, err := st.ResolveMessageWithResolution(cmd.Context(), args[0], actor, resolution)
		if err != nil {
			return err
		}
		return printJSON(cmd, map[string]any{"message": msg, "changed": changed})
	},
}

func init() {
	sendCmd.Flags().StringVar(&sendFlags.sender, "sender", "", "message sender")
	sendCmd.Flags().StringVar(&sendFlags.subject, "subject", "", "message subject")
	sendCmd.Flags().StringVar(&sendFlags.body, "body", "", "message body")
	sendCmd.Flags().StringVar(&sendFlags.channel, "channel", "inbox", "message channel")
	sendCmd.Flags().StringVar(&sendFlags.threadID, "thread-id", "", "optional thread ID")
	sendCmd.Flags().StringVar(&sendFlags.severity, "severity", "info", "severity: info, success, warning, error, action_required")
	sendCmd.Flags().BoolVar(&sendFlags.requiresResponse, "requires-response", false, "mark message as requiring response")
	sendCmd.Flags().StringVar(&sendFlags.idempotencyKey, "idempotency-key", "", "optional idempotency key scoped by sender")

	listCmd.Flags().StringVar(&listFlags.status, "status", "", "filter by status")
	listCmd.Flags().StringVar(&listFlags.channel, "channel", "", "filter by channel")
	listCmd.Flags().StringVar(&listFlags.sender, "sender", "", "filter by sender")
	listCmd.Flags().StringVar(&listFlags.severity, "severity", "", "filter by severity")
	listCmd.Flags().BoolVar(&listFlags.requiresResponse, "requires-response", false, "filter to messages requiring response")
	listCmd.Flags().IntVar(&listFlags.limit, "limit", store.DefaultLimit, "maximum messages to list")
	listCmd.Flags().IntVar(&listFlags.offset, "offset", 0, "messages to skip")
	listCmd.PreRun = func(cmd *cobra.Command, _ []string) { listFlags.hasRequires = cmd.Flags().Changed("requires-response") }
	resolveCmd.Flags().StringVar(&actor, "actor", "user", "lifecycle actor")
	resolveCmd.Flags().StringVar(&resolution, "resolution", "", "optional resolution note")
}

func lifecycleCmd(use, short string, fn func(context.Context, *store.Store, string, string) (store.Message, bool, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  requireOneArg("message id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(activeDBPath())
			if err != nil {
				return fmt.Errorf("opening store: %w", err)
			}
			defer st.Close() //nolint:errcheck
			msg, changed, err := fn(cmd.Context(), st, args[0], actor)
			if err != nil {
				return err
			}
			return printJSON(cmd, map[string]any{"message": msg, "changed": changed})
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "user", "lifecycle actor")
	return cmd
}

func requireOneArg(name string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("requires %s\nUsage: %s", name, cmd.UseLine())
		}
		return nil
	}
}

func printJSON(cmd *cobra.Command, value any) error {
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out))
	return err
}
