package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultDBPathUsesXDGStateHome(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	require.Equal(t, filepath.Join(stateHome, "agent-mailbox", "mailbox.db"), DefaultDBPath())
}

func TestDefaultDBPathFallsBackToHomeLocalState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	require.Equal(t, filepath.Join(home, ".local", "state", "agent-mailbox", "mailbox.db"), DefaultDBPath())
}

func TestTokenPathUsesXDGConfigHome(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	require.Equal(t, filepath.Join(configHome, "agent-mailbox", "auth-token"), TokenPath())
}

func TestTokenPathFallsBackToHomeConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	require.Equal(t, filepath.Join(home, ".config", "agent-mailbox", "auth-token"), TokenPath())
}
