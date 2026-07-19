package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/averycrespi/agent-tools/worktree-sync/cmd"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/daemon"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/service"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	run := func(runCtx context.Context) error {
		paths, err := config.PathsFromEnv()
		if err != nil {
			return err
		}
		controller, err := service.NewFromEnv()
		if err != nil {
			return err
		}
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		logger.Info("wtsd starting", "config", paths.Config)
		return daemon.Run(runCtx, paths, controller, logger)
	}
	err := cmd.ExecuteWTSD(ctx, run, os.Stdout, os.Stderr, os.Args[1:])
	stop()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
