package exec

import (
	"bytes"
	oexec "os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigureProcessUsesCancelableProcessGroup(t *testing.T) {
	cmd := oexec.Command("unused")
	configureProcess(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	require.True(t, cmd.SysProcAttr.Setpgid)
	require.NotNil(t, cmd.Cancel)
	require.Equal(t, time.Second, cmd.WaitDelay)
}

func TestBoundedOutputKeepsDiagnosticTail(t *testing.T) {
	var output boundedOutput
	output.limit = 5
	_, err := output.Write([]byte("hello world"))
	require.NoError(t, err)
	require.Equal(t, "world", string(output.Bytes()))
	require.True(t, output.Truncated())

	_, err = output.Write(bytes.Repeat([]byte("!"), 3))
	require.NoError(t, err)
	require.Equal(t, "ld!!!", string(output.Bytes()))
}
