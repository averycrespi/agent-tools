package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/lifecycle"
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
	if err := newRootCmd().ExecuteContext(coordinator.Context()); err != nil {
		exitCode = 1
	}
	signal.Stop(signals)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
