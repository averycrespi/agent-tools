package main

import (
	"os"

	"github.com/averycrespi/agent-tools/worktree-manager/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
