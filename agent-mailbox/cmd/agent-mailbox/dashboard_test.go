package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDashboardShutdownForcesCloseAfterTimeout(t *testing.T) {
	signals := make(chan struct{}, 1)
	shutdownStarted := make(chan struct{})
	forcedClose := false

	done := make(chan error, 1)
	go func() {
		done <- waitForDashboardShutdown(
			signals,
			make(chan error),
			func(ctx context.Context) error {
				close(shutdownStarted)
				<-ctx.Done()
				return ctx.Err()
			},
			func() error {
				forcedClose = true
				return nil
			},
			10*time.Millisecond,
			func() {},
		)
	}()

	signals <- struct{}{}
	<-shutdownStarted

	require.NoError(t, <-done)
	require.True(t, forcedClose)
}

func TestDashboardShutdownFirstSignalGracefulSecondSignalForced(t *testing.T) {
	signals := make(chan struct{}, 2)
	shutdownStarted := make(chan struct{})
	allowShutdownReturn := make(chan struct{})
	forced := make(chan struct{})

	done := make(chan error, 1)
	go func() {
		done <- waitForDashboardShutdown(
			signals,
			make(chan error),
			func(_ context.Context) error {
				close(shutdownStarted)
				<-allowShutdownReturn
				return context.Canceled
			},
			func() error { return nil },
			time.Second,
			func() { close(forced) },
		)
	}()

	signals <- struct{}{}
	<-shutdownStarted
	signals <- struct{}{}
	<-forced
	close(allowShutdownReturn)

	require.ErrorIs(t, <-done, context.Canceled)
}

func TestValidateDashboardHostAcceptsLoopbackAllowlist(t *testing.T) {
	require.NoError(t, validateDashboardHost("127.0.0.1"))
	require.NoError(t, validateDashboardHost("localhost"))
	require.NoError(t, validateDashboardHost("::1"))
}

func TestValidateDashboardHostRejectsUnsafeHosts(t *testing.T) {
	for _, host := range []string{"", "0.0.0.0", "::", "example.com", "192.168.1.1"} {
		t.Run(host, func(t *testing.T) {
			require.Error(t, validateDashboardHost(host))
		})
	}
}
