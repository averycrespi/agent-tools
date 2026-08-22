package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootCommandIsInertGatewayFoundation(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd()

	require.Equal(t, "mcp-gateway", cmd.Use)
	require.Contains(t, cmd.Short, "deny-by-default")
	require.Empty(t, cmd.Commands())
	require.True(t, cmd.SilenceUsage)
}
