package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type blockingShutdownServer struct {
	closed bool
}

func (s *blockingShutdownServer) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *blockingShutdownServer) Close() error {
	s.closed = true
	return nil
}

func TestShutdownServerForcesCloseAfterTimeout(t *testing.T) {
	srv := &blockingShutdownServer{}

	start := time.Now()
	err := shutdownServer(srv, slog.Default(), 10*time.Millisecond)

	require.NoError(t, err)
	require.True(t, srv.closed)
	require.Less(t, time.Since(start), time.Second)
}
