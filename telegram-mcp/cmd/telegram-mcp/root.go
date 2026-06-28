package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/averycrespi/agent-tools/telegram-mcp/internal/telegram"
	"github.com/averycrespi/agent-tools/telegram-mcp/internal/tools"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

const (
	// Environment variable name only, not a credential value.
	tokenEnv  = "TELEGRAM_MCP_BOT_TOKEN" //nolint:gosec
	chatIDEnv = "TELEGRAM_MCP_CHAT_ID"
)

var (
	apiBase     string
	httpTimeout time.Duration
)

var rootCmd = &cobra.Command{
	Use:   "telegram-mcp",
	Short: "Minimal stdio MCP server for sending Telegram notifications",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		token := os.Getenv(tokenEnv)
		chatID := os.Getenv(chatIDEnv)
		if token == "" {
			return fmt.Errorf("%s is required", tokenEnv)
		}
		if chatID == "" {
			return fmt.Errorf("%s is required", chatIDEnv)
		}

		client := telegram.NewClient(
			token,
			chatID,
			telegram.WithAPIBase(apiBase),
			telegram.WithHTTPClient(&http.Client{Timeout: httpTimeout}),
		)
		handler := tools.NewHandler(client)

		srv := mcpserver.NewMCPServer("telegram-mcp", "0.1.0")
		for _, tool := range handler.Tools() {
			srv.AddTool(tool, handler.Handle)
		}

		slog.Info("starting telegram-mcp stdio server")
		return mcpserver.ServeStdio(srv)
	},
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	rootCmd.Flags().StringVar(&apiBase, "api-base", telegram.DefaultAPIBase, "Telegram Bot API base URL")
	rootCmd.Flags().DurationVar(&httpTimeout, "http-timeout", telegram.DefaultTimeout, "maximum duration for each Telegram API request")
}
