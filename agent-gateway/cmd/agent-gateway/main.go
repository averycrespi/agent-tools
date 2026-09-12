package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/lifecycle"
)

func main() {
	coordinator := lifecycle.New(context.Background(), func() { os.Exit(2) })
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range signals {
			coordinator.Signal()
		}
	}()
	exitCode := 0
	command := newRootCmd()
	// Cobra completion scripts must still register the invoked compatibility name.
	if filepath.Base(os.Args[0]) == "mcp-gateway" {
		command.Use = "mcp-gateway"
	}
	if err := command.ExecuteContext(coordinator.Context()); err != nil {
		exitCode = commandExitCode(err)
	}
	signal.Stop(signals)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
