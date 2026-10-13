package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExternalOriginInvalidSelectionPrecedesInstallation(t *testing.T) {
	for _, args := range [][]string{
		{"--listen", "0.0.0.0:8210"},
		{"--listen", "10.0.0.2:8210"},
		{"--external-origin", ""},
		{"--external-origin", "http://gateway.example"},
		{"--external-origin", "https://gateway.example/path"},
		{"--external-origin", "https://gateway.example", "--listen", "0.0.0.0:8210", "--http-proxy-listen", "127.0.0.1:8210"},
	} {
		t.Run(args[0]+args[1], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "absent")
			command := newRootCmd()
			var output, diagnostic bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&diagnostic)
			command.SetArgs(append([]string{"serve", "--data-dir", root}, args...))
			err := command.ExecuteContext(t.Context())
			require.Error(t, err)
			require.Equal(t, 2, commandExitCode(err))
			require.Empty(t, output.String())
			require.NotEmpty(t, diagnostic.String())
			_, err = os.Lstat(root)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
