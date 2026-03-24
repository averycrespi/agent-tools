package main

import (
	"os"

	"github.com/averycrespi/agent-tools/sandbox-manager/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
