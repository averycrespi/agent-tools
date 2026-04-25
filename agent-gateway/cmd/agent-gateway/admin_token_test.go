package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminTokenRotateCmd_HasLongHelp(t *testing.T) {
	cmd := newAdminTokenRotateCmd()
	require.NotEmpty(t, cmd.Long)
	require.Contains(t, cmd.Long, "Immediate consequences")
	require.Contains(t, cmd.Long, "Recovery")
	require.Contains(t, cmd.Long, "dashboard sessions")
}
