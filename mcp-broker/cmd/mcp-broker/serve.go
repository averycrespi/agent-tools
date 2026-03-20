package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("log-level", "", "log level override (debug, info, warn, error)")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP broker",
	RunE:  runServe,
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func runServe(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if v, _ := cmd.Flags().GetString("log-level"); v != "" {
		cfg.Log.Level = v
	}

	level := parseLogLevel(cfg.Log.Level)
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)

	logger.Info("config loaded", "path", configPath())
	logger.Info("serve is not yet implemented")

	return nil
}
