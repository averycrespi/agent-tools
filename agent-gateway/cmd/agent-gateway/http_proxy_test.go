package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPProxyInvalidSelectionPrecedesInstallationAccess(t *testing.T) {
	for _, authority := range []string{"", "0.0.0.0:8212", "localhost:8212", "127.0.0.1:0", "127.0.0.1:8210"} {
		t.Run(authority, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "must-not-be-created")
			command := newRootCmd()
			var output, diagnostics bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&diagnostics)
			command.SetArgs([]string{"serve", "--data-dir", root, "--http-proxy-listen", authority})
			err := command.ExecuteContext(t.Context())
			require.Error(t, err)
			require.Equal(t, 2, commandExitCode(err))
			require.Empty(t, output.String())
			_, err = os.Lstat(root)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
