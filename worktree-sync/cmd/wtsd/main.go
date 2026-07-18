package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/daemon"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/service"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	paths, err := config.PathsFromEnv()
	if err == nil {
		var controller *service.Service
		controller, err = service.NewFromEnv()
		if err == nil {
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			logger.Info("wtsd starting", "config", paths.Config)
			err = daemon.Run(ctx, paths, controller, logger)
		}
	}
	stop()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
