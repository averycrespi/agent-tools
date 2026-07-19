package exec

import (
	"bytes"
	oexec "os/exec"
	"strings"
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

func TestDiagnosticTailIsBoundedUTF8AndRedactsCredentials(t *testing.T) {
	input := bytes.Repeat([]byte("x"), 5000)
	input = append(input, []byte("\nhttps://user:secret@example.com/repo\nAuthorization: Bearer abc123\ntoken=secret password: hidden\n{\"token\":\"json-secret\", \"password\": \"json-password\"}\n")...)
	input = append(input, 0xff)
	diagnostic := Diagnostic(input)
	require.Contains(t, diagnostic, "[output truncated]")
	require.NotContains(t, diagnostic, "user:secret")
	require.NotContains(t, diagnostic, "abc123")
	require.NotContains(t, diagnostic, "token=secret")
	require.NotContains(t, diagnostic, "password: hidden")
	require.NotContains(t, diagnostic, "json-secret")
	require.NotContains(t, diagnostic, "json-password")
	require.Contains(t, diagnostic, "https://[redacted]@example.com/repo")
	require.Contains(t, diagnostic, "Authorization: [redacted]")
	require.LessOrEqual(t, len([]byte(diagnostic)), 4096+len("[output truncated]\n"))
	require.True(t, bytes.Equal([]byte(string([]rune(diagnostic))), []byte(diagnostic)))

	boundary := Diagnostic([]byte("token=" + strings.Repeat("s", 5000)))
	require.NotContains(t, boundary, strings.Repeat("s", 100))
	require.Contains(t, boundary, "token=[redacted]")
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
