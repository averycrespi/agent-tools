package exec_test

import (
	"testing"

	sbexec "github.com/averycrespi/agent-tools/sandbox-manager/internal/exec"
)

// Verify OSRunner implements Runner at compile time.
var _ sbexec.Runner = (*sbexec.OSRunner)(nil)

func TestOSRunner_Run_EchoHello(t *testing.T) {
	r := sbexec.NewOSRunner()
	out, err := r.Run("echo", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "hello\n" {
		t.Fatalf("expected %q, got %q", "hello\n", string(out))
	}
}
