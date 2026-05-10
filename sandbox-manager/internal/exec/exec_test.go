package exec_test

import (
	"io"
	"testing"

	sbexec "github.com/averycrespi/agent-tools/sandbox-manager/internal/exec"
	"github.com/stretchr/testify/require"
)

// Verify OSRunner implements Runner at compile time.
var _ sbexec.Runner = (*sbexec.OSRunner)(nil)

func TestOSRunner_Run_EchoHello(t *testing.T) {
	r := sbexec.NewOSRunner()
	out, err := r.Run("echo", "hello")
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(out))
}

func TestOSRunner_StartPiped_SeparatesStdioAndDeliversStdin(t *testing.T) {
	r := sbexec.NewOSRunner()
	proc, err := r.StartPiped("sh", "-c", "cat; echo err >&2")
	require.NoError(t, err)

	_, err = io.WriteString(proc.Stdin(), "hello\n")
	require.NoError(t, err)
	require.NoError(t, proc.Stdin().Close())

	stdout, err := io.ReadAll(proc.Stdout())
	require.NoError(t, err)
	stderr, err := io.ReadAll(proc.Stderr())
	require.NoError(t, err)
	require.NoError(t, proc.Wait())

	require.Equal(t, "hello\n", string(stdout))
	require.Equal(t, "err\n", string(stderr))
}
