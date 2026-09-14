package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceRenderedCommandInterpretations(t *testing.T) {
	d := definition{Settings: Settings{Binary: "/bin/gateway with spaces", DataDir: "/account/data root", Listen: "127.0.0.1:8210"}}
	cases := []struct {
		arguments        string
		related, unknown bool
	}{
		{"--data-dir ROOT status", false, false},
		{"--data-dir=ROOT status", false, false},
		{"serve --data-dir ROOT-old --listen 127.0.0.1:8210", false, false},
		{"serve --data-dir ROOT old --listen 127.0.0.1:8210", false, false},
		{"--data-dir ROOT serve --listen 127.0.0.1:8210", true, false},
		{"serve --listen 127.0.0.1:8210 --data-dir=ROOT", true, false},
		{"serve --data-dir ROOT --listen 127.0.0.1:8210 --help=false", true, false},
		{"serve --data-dir ROOT -h=false", true, false},
		{"serve --data-dir=ROOT -h=false --listen 127.0.0.1:8210", true, false},
		{"serve -h=false --data-dir ROOT", true, false},
		{"--data-dir ROOT serve -h=false", true, false},
		{"serve --data-dir ROOT -h", false, false},
		{"serve --data-dir ROOT -h=true", false, false},
		{"serve --data-dir ROOT --listen 127.0.0.1:8210 --help", false, false},
		{"serve --data-dir /another/root --data-dir ROOT", true, false},
		{"serve --data-dir ROOT --data-dir /another/root", false, false},
		{"serve --listen 127.0.0.1:8210", false, true},
		{"serve --data-dir ../relative", false, true},
	}
	for _, test := range cases {
		t.Run(test.arguments, func(t *testing.T) {
			command := d.Binary + " " + strings.ReplaceAll(test.arguments, "ROOT", d.DataDir)
			related, err := relevantCommand(command, d)
			if test.unknown {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.related, related)
		})
	}
	related, err := relevantCommand(filepath.Base(d.Binary)+" status", d)
	require.NoError(t, err)
	require.False(t, related)
	for _, root := range []string{"/data/with --listen words ", "/data/with serve words", "/data/with status words"} {
		d.DataDir = root
		related, err = relevantCommand(d.Binary+" --data-dir "+root+" serve --listen 127.0.0.1:8210", d)
		require.NoError(t, err)
		require.True(t, related)
		related, err = relevantCommand(d.Binary+" --data-dir "+root+" status", d)
		require.NoError(t, err)
		require.False(t, related)
	}
	_, err = relevantCommand(d.Binary+" serve "+strings.Repeat("--json ", 513), d)
	require.Error(t, err)
}
