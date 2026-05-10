package cmd

import (
	"bytes"
	"io"
	"testing"
	"time"

	sbexec "github.com/averycrespi/agent-tools/sandbox-manager/internal/exec"
	"github.com/stretchr/testify/require"
)

type blockingReader struct {
	done chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{done: make(chan struct{})}
}

func (r *blockingReader) Read(_ []byte) (int, error) {
	<-r.done
	return 0, io.EOF
}

func (r *blockingReader) Close() error {
	close(r.done)
	return nil
}

type fakeExecProcess struct {
	stdin  *nopWriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func newFakeExecProcess(stdout, stderr string) *fakeExecProcess {
	return &fakeExecProcess{
		stdin:  &nopWriteCloser{},
		stdout: io.NopCloser(bytes.NewBufferString(stdout)),
		stderr: io.NopCloser(bytes.NewBufferString(stderr)),
	}
}

func (p *fakeExecProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *fakeExecProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *fakeExecProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *fakeExecProcess) Wait() error           { return nil }
func (p *fakeExecProcess) Kill() error           { return nil }

var _ sbexec.Process = (*fakeExecProcess)(nil)

type nopWriteCloser struct{}

func (w *nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (w *nopWriteCloser) Close() error                { return nil }

func TestRunExecProcessReturnsWhenProcessExitsWithOpenStdin(t *testing.T) {
	proc := newFakeExecProcess("out\n", "err\n")
	stdin := newBlockingReader()
	defer func() { require.NoError(t, stdin.Close()) }()
	var stdout, stderr bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- runExecProcess(proc, stdin, &stdout, &stderr)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runExecProcess waited for stdin after the process exited")
	}

	require.Equal(t, "out\n", stdout.String())
	require.Equal(t, "err\n", stderr.String())
}

func TestRunExecProcessCopiesStdinBeforeWaitingForProcess(t *testing.T) {
	stdin := bytes.NewBufferString("hello\n")
	proc := newRecordingProcess()

	err := runExecProcess(proc, stdin, io.Discard, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "hello\n", proc.stdin.String())
	require.True(t, proc.stdin.closed)
}

type recordingProcess struct {
	stdin  *recordingWriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func newRecordingProcess() *recordingProcess {
	return &recordingProcess{
		stdin:  &recordingWriteCloser{closedCh: make(chan struct{})},
		stdout: io.NopCloser(bytes.NewBuffer(nil)),
		stderr: io.NopCloser(bytes.NewBuffer(nil)),
	}
}

func (p *recordingProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *recordingProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *recordingProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *recordingProcess) Wait() error {
	<-p.stdin.closedCh
	return nil
}
func (p *recordingProcess) Kill() error { return nil }

type recordingWriteCloser struct {
	bytes.Buffer
	closed   bool
	closedCh chan struct{}
}

func (w *recordingWriteCloser) Close() error {
	w.closed = true
	close(w.closedCh)
	return nil
}

type fakeExecService struct{ proc *fakeExecProcess }

func (s fakeExecService) ExecPiped(workdir string, args ...string) (sbexec.Process, error) {
	return s.proc, nil
}

func TestRunExecCommandUsesNonHangingProcessHelper(t *testing.T) {
	proc := newFakeExecProcess("out\n", "")
	stdin := newBlockingReader()
	defer func() { require.NoError(t, stdin.Close()) }()
	var stdout bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- runExecCommand(fakeExecService{proc: proc}, "/work", []string{"pwd"}, stdin, &stdout, io.Discard)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runExecCommand waited for stdin after the process exited")
	}
	require.Equal(t, "out\n", stdout.String())
}
