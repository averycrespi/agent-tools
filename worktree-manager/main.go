package main

import (
	"log/slog"
	"os"

	"github.com/averycrespi/agent-tools/worktree-manager/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}
