package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceptanceTemporaryRootsDetectsRootsCreatedDuringRun(t *testing.T) {
	before, err := acceptanceTemporaryRoots()
	require.NoError(t, err)
	created, err := os.MkdirTemp("", "mcp-gateway-ui-development-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(created) })

	after, err := acceptanceTemporaryRoots()
	require.NoError(t, err)
	assert.False(t, containsNoNewTemporaryRoots(before, after))
	require.NoError(t, os.RemoveAll(created))
	restored, err := acceptanceTemporaryRoots()
	require.NoError(t, err)
	assert.True(t, containsNoNewTemporaryRoots(before, restored))
}

func TestAcceptanceCommandRejectsRemovedModes(t *testing.T) {
	for _, argument := range []string{"--profile", "--profile=retired", "--task", "--milestone", "--qualify-external"} {
		assert.Equal(t, 2, run([]string{argument}), argument)
	}
}

func TestAcceptanceCommandRequiresOneClosedInterface(t *testing.T) {
	assert.Equal(t, 2, run(nil))
	assert.Equal(t, 2, run([]string{"unknown"}))
	assert.Equal(t, 2, run([]string{"accept"}))
	assert.Equal(t, 2, run([]string{"adopt-acceptance-report"}))
	assert.Equal(t, 2, run([]string{"qualify-external-evidence", "extra"}))
}
