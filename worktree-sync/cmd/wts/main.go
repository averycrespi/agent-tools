package main

import (
	"context"
	"fmt"
	"os"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/service"
)

func main() {
	controller, err := service.NewFromEnv()
	if err == nil {
		err = app.ExecuteWTS(context.Background(), controller, os.Stdout, os.Stderr, os.Args[1:])
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
