package exec_test

import (
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
