package paths_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func TestCheckOwnerAndMode(t *testing.T) {
	tmp := t.TempDir()
	mustChmod := func(p string, m os.FileMode) { require.NoError(t, os.Chmod(p, m)) }
	cases := []struct {
		mode    os.FileMode
		wantErr bool
	}{
		{0o700, false},
		{0o750, true},
		{0o755, true},
		{0o777, true},
	}
	for _, c := range cases {
		t.Run(c.mode.String(), func(t *testing.T) {
			d := filepath.Join(tmp, c.mode.String())
			require.NoError(t, os.Mkdir(d, 0o700))
			mustChmod(d, c.mode)
			err := paths.CheckOwnerAndMode(d, 0o700)
			if c.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCheckOwnerAndMode_MissingPath(t *testing.T) {
	tmp := t.TempDir()
	err := paths.CheckOwnerAndMode(filepath.Join(tmp, "does-not-exist"), 0o700)
	require.Error(t, err)
}
