package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

type controller interface {
	Execute(context.Context, app.Request) (string, error)
}

func Run(ctx context.Context, paths config.Paths, service controller, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	lock, err := state.Acquire(ctx, filepath.Join(paths.State, "daemon.lock"))
	if err != nil {
		return fmt.Errorf("another wtsd instance is running or daemon lock failed: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating filesystem watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o700); err != nil {
		return err
	}
	_ = watcher.Add(filepath.Dir(paths.Config))

	reconcileNow := func(reason string) (config.Config, error) {
		logger.Info("reconciling", "reason", reason)
		output, reconcileErr := service.Execute(ctx, app.Request{Action: "reconcile"})
		if reconcileErr != nil {
			logger.Warn("reconciliation degraded", "reason", reason, "error", reconcileErr)
		} else {
			logger.Info("reconciliation complete", "summary", output)
		}
		cfg, loadErr := config.Load(paths.Config)
		if loadErr != nil {
			logger.Warn("config invalid; mutations paused", "error", loadErr)
			return config.Config{}, loadErr
		}
		desiredWatches := map[string]bool{filepath.Dir(paths.Config): true}
		for _, repo := range cfg.Repositories {
			desiredWatches[repo.CommonGitDir] = true
			for _, root := range repo.AllowedRoots {
				desiredWatches[root] = true
			}
		}
		for _, watched := range watcher.WatchList() {
			if !desiredWatches[watched] {
				if removeErr := watcher.Remove(watched); removeErr != nil {
					logger.Warn("removing filesystem watch", "path", watched, "error", removeErr)
				}
			}
		}
		currentWatches := make(map[string]bool)
		for _, watched := range watcher.WatchList() {
			currentWatches[watched] = true
		}
		for path := range desiredWatches {
			if !currentWatches[path] {
				if addErr := watcher.Add(path); addErr != nil {
					logger.Warn("adding filesystem watch", "path", path, "error", addErr)
				}
			}
		}
		return cfg, reconcileErr
	}
	cfg, _ := reconcileNow("startup")
	interval := 30 * time.Second
	debounce := 250 * time.Millisecond
	if parsed, parseErr := time.ParseDuration(cfg.Global.ReconcileInterval); parseErr == nil && parsed > 0 {
		interval = parsed
	}
	if parsed, parseErr := time.ParseDuration(cfg.Global.Debounce); parseErr == nil && parsed > 0 {
		debounce = parsed
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var timer *time.Timer
	var timerChannel <-chan time.Time
	queue := func() {
		if timer == nil {
			timer = time.NewTimer(debounce)
			timerChannel = timer.C
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(debounce)
		timerChannel = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			logger.Info("daemon shutting down")
			return nil
		case <-ticker.C:
			cfg, _ = reconcileNow("interval")
			if parsed, parseErr := time.ParseDuration(cfg.Global.ReconcileInterval); parseErr == nil && parsed > 0 && parsed != interval {
				interval = parsed
				ticker.Reset(interval)
			}
		case <-timerChannel:
			timerChannel = nil
			cfg, _ = reconcileNow("filesystem event")
			if parsed, parseErr := time.ParseDuration(cfg.Global.Debounce); parseErr == nil && parsed > 0 {
				debounce = parsed
			}
		case _, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("filesystem watcher closed")
			}
			queue()
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return fmt.Errorf("filesystem watcher error channel closed")
			}
			logger.Warn("filesystem watcher error", "error", watchErr)
			queue()
		}
	}
}
