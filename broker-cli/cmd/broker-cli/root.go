package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/averycrespi/agent-tools/broker-cli/internal/cache"
	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/averycrespi/agent-tools/broker-cli/internal/output"
	"github.com/averycrespi/agent-tools/broker-cli/internal/tree"
	"github.com/spf13/cobra"
)

var noCache bool

var rootCmd = &cobra.Command{
	Use:           "broker-cli",
	Short:         "CLI frontend for the MCP broker",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `broker-cli connects to an MCP broker and exposes its tools as CLI subcommands.

Environment:
  MCP_BROKER_ENDPOINT    Broker URL (required)
  MCP_BROKER_AUTH_TOKEN  Bearer token (required)`,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&noCache, "no-cache", false, "Bypass tool discovery cache")
}

func buildTree() error {
	endpoint := os.Getenv("MCP_BROKER_ENDPOINT")
	token := os.Getenv("MCP_BROKER_AUTH_TOKEN")
	if endpoint == "" {
		return fmt.Errorf("MCP_BROKER_ENDPOINT is not set")
	}
	if token == "" {
		return fmt.Errorf("MCP_BROKER_AUTH_TOKEN is not set")
	}

	toolCache := cache.New(30 * time.Second)
	var tools []client.Tool

	if !noCache {
		if cached, ok := toolCache.Get(endpoint); ok {
			tools = cached
		}
	}

	if tools == nil {
		ctx := context.Background()
		c, err := client.New(ctx, endpoint+"/mcp", token)
		if err != nil {
			return fmt.Errorf("connect to broker: %w", err)
		}
		defer func() {
			if cerr := c.Close(); cerr != nil {
				fmt.Fprintf(os.Stderr, "close client: %v\n", cerr)
			}
		}()

		tools, err = c.ListTools(ctx)
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}

		_ = toolCache.Set(endpoint, tools) // cache miss is non-fatal
	}

	exec := func(toolName string, args map[string]any) error {
		return callTool(endpoint, token, toolName, args)
	}

	tree.Build(rootCmd, tools, exec)
	rootCmd.AddCommand(newGrantCmd(endpoint, token))
	return nil
}

func callTool(endpoint, token, toolName string, args map[string]any) error {
	ctx := context.Background()
	c, err := client.New(ctx, endpoint+"/mcp", token)
	if err != nil {
		return fmt.Errorf("connect to broker: %w", err)
	}
	defer func() {
		if cerr := c.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "close client: %v\n", cerr)
		}
	}()

	result, err := c.CallTool(ctx, toolName, args)

	if err != nil {
		return err
	}

	if result.IsError {
		if len(result.Content) > 0 {
			return errors.New(result.Content[0].Text)
		}
		return fmt.Errorf("tool call failed")
	}

	out, err := output.Format(result)
	if err != nil {
		return fmt.Errorf("format output: %w", err)
	}
	fmt.Println(out)
	return nil
}

// writeError prints a JSON error object to stderr.
func writeError(err error) {
	data, _ := json.Marshal(map[string]string{"error": err.Error()})
	fmt.Fprintln(os.Stderr, string(data))
}
