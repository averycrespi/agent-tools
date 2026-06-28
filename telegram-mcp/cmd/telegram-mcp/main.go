package main

import (
	"log/slog"
	"os"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}
