package main

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-dispatch/internal/control"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/store"
	"github.com/stretchr/testify/require"
)

func TestControlAllowedAllowsForceStopWhileStopping(t *testing.T) {
	require.NoError(t, controlAllowed(store.StatusStopping, control.Request{Operation: control.OpStop, Force: true}))
}

func TestControlAllowedRejectsSteerWhileStopping(t *testing.T) {
	err := controlAllowed(store.StatusStopping, control.Request{Operation: control.OpSteer})
	require.Error(t, err)
}
