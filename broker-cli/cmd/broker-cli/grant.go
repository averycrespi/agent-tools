package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	grantsclient "github.com/averycrespi/agent-tools/broker-cli/internal/grants"
)

func newGrantCmd(endpoint, authToken string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Manage time-bounded authorization grants",
	}
	cmd.AddCommand(newGrantCreateCmd(endpoint, authToken))
	cmd.AddCommand(newGrantListCmd(endpoint, authToken))
	cmd.AddCommand(newGrantRevokeCmd(endpoint, authToken))
	return cmd
}

func newGrantCreateCmd(endpoint, authToken string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new grant",
		Long: `Create a new time-bounded authorization grant.

Flags are parsed manually to support multiple --tool sections:

  --ttl <duration>          Required. Grant lifetime (e.g. 1h, 30m).
  --description <text>      Optional human-readable label.
  --tool <name>             Open a new tool section. Repeatable.
  --arg-equal  <k=v>        Constrain arg to exact value.
  --arg-match  <k=pattern>  Constrain arg to regex pattern.
  --arg-enum   <k=a,b,c>    Constrain arg to one of the listed values.
  --arg-schema-file <path>  Load arg schema from a JSON file (exclusive).

Example:
  broker-cli grant create \
    --ttl 1h --description "push feat/foo" \
    --tool git.git_push --arg-equal branch=feat/foo --arg-equal force=false \
    --tool git.git_fetch`,
		// DisableFlagParsing so cobra does not try to parse our custom flags.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ttl, description, remaining, err := parseGrantGlobalFlags(args)
			if err != nil {
				return err
			}
			if ttl <= 0 {
				return fmt.Errorf("--ttl is required")
			}

			_, groups, err := grantsclient.SplitByTool(remaining)
			if err != nil {
				return err
			}

			// Fetch tool input schemas for pre-submit validation.
			// TODO: skip validation if listing tools fails (server validates anyway).
			toolSchemas, fetchErr := fetchToolInputSchemas(endpoint, authToken)

			var entries []grantsclient.Entry
			for _, g := range groups {
				schema, err := grantsclient.BuildSchema(g)
				if err != nil {
					return fmt.Errorf("tool %q: %w", g.Tool, err)
				}

				if fetchErr == nil {
					toolSchema, ok := toolSchemas[g.Tool]
					if !ok {
						return fmt.Errorf("unknown tool %q", g.Tool)
					}
					if err := grantsclient.ValidateAgainstInputSchema(schema, toolSchema); err != nil {
						return fmt.Errorf("tool %q: %w", g.Tool, err)
					}
				}

				entries = append(entries, grantsclient.Entry{Tool: g.Tool, ArgSchema: schema})
			}

			resp, err := grantsclient.NewClient(endpoint, authToken).Create(cmd.Context(), grantsclient.CreateRequest{
				Description: description,
				TTL:         grantsclient.Duration(ttl),
				Entries:     entries,
			})
			if err != nil {
				return err
			}
			printGrantCreated(os.Stdout, resp)
			return nil
		},
	}
	return cmd
}

// parseGrantGlobalFlags walks args and extracts --ttl and --description,
// returning the remainder for SplitByTool.
func parseGrantGlobalFlags(args []string) (ttl time.Duration, description string, remaining []string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ttl":
			if i+1 >= len(args) {
				return 0, "", nil, fmt.Errorf("--ttl requires a value")
			}
			i++
			ttl, err = time.ParseDuration(args[i])
			if err != nil {
				return 0, "", nil, fmt.Errorf("--ttl: %w", err)
			}
		case "--description":
			if i+1 >= len(args) {
				return 0, "", nil, fmt.Errorf("--description requires a value")
			}
			i++
			description = args[i]
		default:
			remaining = append(remaining, args[i])
		}
	}
	return ttl, description, remaining, nil
}

// fetchToolInputSchemas connects to the broker, lists tools, and returns a map
// from fully-qualified tool name to its InputSchema as JSON.
func fetchToolInputSchemas(endpoint, authToken string) (map[string]json.RawMessage, error) {
	ctx := context.Background()

	c, err := client.New(ctx, endpoint+"/mcp", authToken)
	if err != nil {
		return nil, fmt.Errorf("connect to broker: %w", err)
	}
	defer func() {
		_ = c.Close()
	}()

	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	schemas := make(map[string]json.RawMessage, len(tools))
	for _, t := range tools {
		raw, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal schema for %q: %w", t.Name, err)
		}
		schemas[t.Name] = raw
	}
	return schemas, nil
}

func printGrantCreated(w io.Writer, r *grantsclient.CreateResponse) {
	ew := &errWriter{w: w}
	ew.printf("Grant created.\n")
	ew.printf("  ID:          %s\n", r.ID)
	ew.printf("  Token:       %s   <- copy now; will not be shown again\n", r.Token)
	ew.printf("  Tools:       %s\n", strings.Join(r.Tools, ", "))
	ttl := r.ExpiresAt.Sub(r.CreatedAt).Round(time.Second)
	ew.printf("  Expires:     %s (in %s)\n", r.ExpiresAt.Format(time.RFC3339), ttl)
	if r.Description != "" {
		ew.printf("  Description: %s\n", r.Description)
	}
	ew.printf("\nExport it for an agent session:\n  export MCP_BROKER_GRANT_TOKEN=%s\n", r.Token)
}

// errWriter is a write-once error-capturing io.Writer wrapper.
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) printf(format string, args ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintf(ew.w, format, args...)
}

func newGrantListCmd(endpoint, authToken string) *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List grants",
		RunE: func(cmd *cobra.Command, args []string) error {
			grants, err := grantsclient.NewClient(endpoint, authToken).List(cmd.Context(), all)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(grants)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "ID\tTOOLS\tEXPIRES\tSTATUS\tDESCRIPTION")
			for _, g := range grants {
				tools := make([]string, len(g.Entries))
				for i, e := range g.Entries {
					tools[i] = e.Tool
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					g.ID, strings.Join(tools, ","),
					g.ExpiresAt.Format(time.RFC3339),
					statusOf(&g), g.Description)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include expired and revoked grants")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON output")
	return cmd
}

func statusOf(g *grantsclient.Grant) string {
	if g.RevokedAt != nil {
		return "revoked"
	}
	if time.Now().After(g.ExpiresAt) {
		return "expired"
	}
	return "active"
}

func newGrantRevokeCmd(endpoint, authToken string) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <grant-id>",
		Short: "Revoke a grant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := grantsclient.NewClient(endpoint, authToken).Revoke(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(os.Stdout, "revoked %s\n", args[0])
			return nil
		},
	}
}
