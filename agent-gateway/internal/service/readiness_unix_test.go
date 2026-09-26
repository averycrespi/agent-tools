//go:build darwin || linux

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServiceStartObservesReadinessWithoutReplay(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	probes := 0
	f.m.probe = func(context.Context, string) string {
		probes++
		if probes == 1 {
			return "not-ready"
		}
		return "ready"
	}
	result, err := f.m.execute(t.Context(), "start", Changes{})
	require.NoError(t, err)
	require.Equal(t, "launch-accepted", result.Launchd)
	require.Equal(t, "ready", result.Readiness)
	require.Equal(t, 2, probes)
	require.Len(t, f.mutations, 1)
}

func TestServiceReadinessDeadlinePreservesLaunchAcceptance(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	f.m.probe = func(context.Context, string) string { cancel(); return "not-ready" }
	result, err := f.m.execute(ctx, "start", Changes{})
	require.ErrorContains(t, err, "doctor")
	require.Equal(t, "launch-accepted", result.Launchd)
	require.Equal(t, "not-ready", result.Readiness)
	require.NotEmpty(t, result.Stdout)
	require.NotEmpty(t, result.Stderr)
	require.Len(t, f.mutations, 1)
}
