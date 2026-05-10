package cmd

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExitCodeForErrorPreservesGuestExitStatus(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 42").Run()
	require.Error(t, err)
	require.Equal(t, 42, ExitCodeForError(err))
}

func TestExitCodeForErrorDefaultsToOne(t *testing.T) {
	require.Equal(t, 1, ExitCodeForError(assertionError("boom")))
}

type assertionError string

func (e assertionError) Error() string { return string(e) }
