package sandbox_test

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/config"
	sbexec "github.com/averycrespi/agent-tools/sandbox-manager/internal/exec"
	"github.com/averycrespi/agent-tools/sandbox-manager/internal/lima"
	"github.com/averycrespi/agent-tools/sandbox-manager/internal/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var nopLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

type mockLima struct {
	mock.Mock
}

var _ sandbox.LimaClient = (*mockLima)(nil)

func (m *mockLima) Status() (lima.Status, error) {
	args := m.Called()
	return args.Get(0).(lima.Status), args.Error(1)
}

func (m *mockLima) Create(templatePath string) error {
	return m.Called(templatePath).Error(0)
}

func (m *mockLima) Start() error {
	return m.Called().Error(0)
}

func (m *mockLima) Stop() error {
	return m.Called().Error(0)
}

func (m *mockLima) Delete() error {
	return m.Called().Error(0)
}

func (m *mockLima) UpdateMounts(mounts []string) error {
	return m.Called(mounts).Error(0)
}

func (m *mockLima) Copy(localPath, guestPath string, recursive bool) error {
	return m.Called(localPath, guestPath, recursive).Error(0)
}

func (m *mockLima) Exec(workdir string, args ...string) ([]byte, error) {
	called := m.Called(workdir, args)
	return called.Get(0).([]byte), called.Error(1)
}

func (m *mockLima) ExecPiped(workdir string, args ...string) (sbexec.Process, error) {
	called := m.Called(workdir, args)
	proc, _ := called.Get(0).(sbexec.Process)
	return proc, called.Error(1)
}

func (m *mockLima) Shell(args ...string) error {
	return m.Called(args).Error(0)
}

func TestService_Status_Running(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	status, err := svc.Status()
	require.NoError(t, err)
	assert.Equal(t, lima.StatusRunning, status)
}

type fakeProcess struct{}

func (fakeProcess) Stdin() io.WriteCloser { return nopWriteCloser{} }
func (fakeProcess) Stdout() io.ReadCloser { return io.NopCloser(&emptyReader{}) }
func (fakeProcess) Stderr() io.ReadCloser { return io.NopCloser(&emptyReader{}) }
func (fakeProcess) Wait() error           { return nil }
func (fakeProcess) Kill() error           { return nil }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

type emptyReader struct{}

func (*emptyReader) Read(_ []byte) (int, error) { return 0, io.EOF }

func TestService_ExecPiped_Running(t *testing.T) {
	ml := new(mockLima)
	proc := fakeProcess{}
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("ExecPiped", "/work", []string{"cat"}).Return(proc, nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	got, err := svc.ExecPiped("/work", "cat")

	require.NoError(t, err)
	assert.Equal(t, proc, got)
}

func TestService_ExecPiped_NotRunning(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusStopped, nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	_, err := svc.ExecPiped("/work", "cat")

	assert.ErrorContains(t, err, "VM not running")
	ml.AssertNotCalled(t, "ExecPiped", mock.Anything, mock.Anything)
}

func TestService_Start_Stopped(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusStopped, nil)
	ml.On("Start").Return(nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	require.NoError(t, svc.Start())
	ml.AssertCalled(t, "Start")
}

func TestService_Start_NotCreated(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusNotCreated, nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	err := svc.Start()
	assert.ErrorContains(t, err, "not created")
}

func TestService_Start_AlreadyRunning(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	require.NoError(t, svc.Start())
	ml.AssertNotCalled(t, "Start")
}

func TestService_Restart_Running_SyncsMounts(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Stop").Return(nil)
	ml.On("UpdateMounts", []string{"/Users/test/work"}).Return(nil)
	ml.On("Start").Return(nil)

	cfg := config.Default()
	cfg.Mounts = []string{"/Users/test/work"}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Restart())
	ml.AssertCalled(t, "Stop")
	ml.AssertCalled(t, "UpdateMounts", []string{"/Users/test/work"})
	ml.AssertCalled(t, "Start")
}

func TestService_Restart_Stopped_SyncsMounts(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusStopped, nil)
	ml.On("UpdateMounts", []string{"/Users/test/work"}).Return(nil)
	ml.On("Start").Return(nil)

	cfg := config.Default()
	cfg.Mounts = []string{"/Users/test/work"}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Restart())
	ml.AssertNotCalled(t, "Stop")
	ml.AssertCalled(t, "UpdateMounts", []string{"/Users/test/work"})
	ml.AssertCalled(t, "Start")
}

func TestService_Restart_NotCreated(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusNotCreated, nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	err := svc.Restart()
	assert.ErrorContains(t, err, "not created")
	ml.AssertNotCalled(t, "Stop")
	ml.AssertNotCalled(t, "Start")
}

func TestService_Stop_Running(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Stop").Return(nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	require.NoError(t, svc.Stop())
	ml.AssertCalled(t, "Stop")
}

func TestService_Stop_NotCreated(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusNotCreated, nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	require.NoError(t, svc.Stop())
	ml.AssertNotCalled(t, "Stop")
}

func TestService_Destroy_Running(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Stop").Return(nil)
	ml.On("Delete").Return(nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	require.NoError(t, svc.Destroy())
	ml.AssertCalled(t, "Stop")
	ml.AssertCalled(t, "Delete")
}

func TestService_Destroy_NotCreated(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusNotCreated, nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	require.NoError(t, svc.Destroy())
	ml.AssertNotCalled(t, "Delete")
}

func TestService_Provision_CopyPaths(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), ".zshrc")
	require.NoError(t, os.WriteFile(tmpFile, []byte(""), 0o644))
	// EvalSymlinks resolves /var -> /private/var on macOS.
	resolvedFile, err := filepath.EvalSymlinks(tmpFile)
	require.NoError(t, err)

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Exec", "/", []string{"mkdir", "-p", filepath.Dir(tmpFile)}).Return([]byte(""), nil)
	ml.On("Copy", resolvedFile, tmpFile, false).Return(nil)

	cfg := config.Default()
	cfg.CopyPaths = []string{tmpFile}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Provision())
	ml.AssertCalled(t, "Copy", resolvedFile, tmpFile, false)
}

func TestService_Provision_CopyPaths_Directory(t *testing.T) {
	tmpDir := filepath.Join(t.TempDir(), "commands")
	require.NoError(t, os.MkdirAll(tmpDir, 0o755))
	resolvedDir, err := filepath.EvalSymlinks(tmpDir)
	require.NoError(t, err)

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Exec", "/", []string{"mkdir", "-p", tmpDir}).Return([]byte(""), nil)
	ml.On("Copy", resolvedDir, tmpDir, true).Return(nil)

	cfg := config.Default()
	cfg.CopyPaths = []string{tmpDir}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Provision())
	ml.AssertCalled(t, "Copy", resolvedDir, tmpDir, true)
}

func TestService_Provision_NotRunning(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusStopped, nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	err := svc.Provision()
	assert.ErrorContains(t, err, "not running")
}

func TestService_Create_AlreadyRunning_SyncsMounts(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), ".zshrc")
	require.NoError(t, os.WriteFile(tmpFile, []byte(""), 0o644))
	resolvedFile, err := filepath.EvalSymlinks(tmpFile)
	require.NoError(t, err)

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil).Once()
	ml.On("Stop").Return(nil)
	ml.On("UpdateMounts", []string{"/Users/test/work"}).Return(nil)
	ml.On("Start").Return(nil)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Exec", "/", []string{"mkdir", "-p", filepath.Dir(tmpFile)}).Return([]byte(""), nil)
	ml.On("Copy", resolvedFile, tmpFile, false).Return(nil)

	cfg := config.Default()
	cfg.Mounts = []string{"/Users/test/work"}
	cfg.CopyPaths = []string{tmpFile}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Create())
	ml.AssertCalled(t, "Stop")
	ml.AssertCalled(t, "UpdateMounts", []string{"/Users/test/work"})
	ml.AssertCalled(t, "Start")
	ml.AssertCalled(t, "Copy", resolvedFile, tmpFile, false)
}

func TestService_Create_Stopped_SyncsMounts(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusStopped, nil).Once()
	ml.On("UpdateMounts", []string{"/Users/test/work"}).Return(nil)
	ml.On("Start").Return(nil)
	ml.On("Status").Return(lima.StatusRunning, nil)

	cfg := config.Default()
	cfg.Mounts = []string{"/Users/test/work"}
	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Create())
	ml.AssertCalled(t, "UpdateMounts", []string{"/Users/test/work"})
	ml.AssertCalled(t, "Start")
}

func TestService_Provision_Scripts(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(""), 0o644))

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Copy", scriptPath, "/tmp/sb-provision-script", false).Return(nil)
	ml.On("Exec", "/", []string{"chmod", "+x", "/tmp/sb-provision-script"}).Return([]byte(""), nil)
	ml.On("Exec", "/", []string{"/tmp/sb-provision-script"}).Return([]byte(""), nil)
	ml.On("Exec", "/", []string{"rm", "-f", "/tmp/sb-provision-script"}).Return([]byte(""), nil)

	cfg := config.Default()
	cfg.Scripts = []string{scriptPath}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Provision())
	ml.AssertCalled(t, "Copy", scriptPath, "/tmp/sb-provision-script", false)
	ml.AssertCalled(t, "Exec", "/", []string{"chmod", "+x", "/tmp/sb-provision-script"})
	ml.AssertCalled(t, "Exec", "/", []string{"/tmp/sb-provision-script"})
	ml.AssertCalled(t, "Exec", "/", []string{"rm", "-f", "/tmp/sb-provision-script"})
}

func TestService_Provision_Scripts_TildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	expanded := filepath.Join(home, "setup.sh")
	require.NoError(t, os.WriteFile(expanded, []byte(""), 0o644))

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Copy", expanded, "/tmp/sb-provision-script", false).Return(nil)
	ml.On("Exec", "/", []string{"chmod", "+x", "/tmp/sb-provision-script"}).Return([]byte(""), nil)
	ml.On("Exec", "/", []string{"/tmp/sb-provision-script"}).Return([]byte(""), nil)
	ml.On("Exec", "/", []string{"rm", "-f", "/tmp/sb-provision-script"}).Return([]byte(""), nil)

	cfg := config.Default()
	cfg.Scripts = []string{"~/setup.sh"}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Provision())
	ml.AssertCalled(t, "Copy", expanded, "/tmp/sb-provision-script", false)
}

func TestService_Provision_ScriptExecError(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(""), 0o644))

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Copy", scriptPath, "/tmp/sb-provision-script", false).Return(nil)
	ml.On("Exec", "/", []string{"chmod", "+x", "/tmp/sb-provision-script"}).Return([]byte(""), nil)
	ml.On("Exec", "/", []string{"/tmp/sb-provision-script"}).Return([]byte(""), fmt.Errorf("exit code 1"))

	cfg := config.Default()
	cfg.Scripts = []string{scriptPath}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	err := svc.Provision()
	assert.ErrorContains(t, err, "failed to run script")
}

func TestService_Provision_ScriptMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.sh")

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)

	cfg := config.Default()
	cfg.Scripts = []string{missing}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	err := svc.Provision()
	assert.ErrorContains(t, err, "does not exist")
	ml.AssertNotCalled(t, "Copy", mock.Anything, mock.Anything, mock.Anything)
}

func TestService_Provision_CopyError(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), ".zshrc")
	require.NoError(t, os.WriteFile(tmpFile, []byte(""), 0o644))
	resolvedFile, err := filepath.EvalSymlinks(tmpFile)
	require.NoError(t, err)

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Exec", "/", []string{"mkdir", "-p", filepath.Dir(tmpFile)}).Return([]byte(""), nil)
	ml.On("Copy", resolvedFile, tmpFile, false).Return(fmt.Errorf("copy failed"))

	cfg := config.Default()
	cfg.CopyPaths = []string{tmpFile}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	err = svc.Provision()
	assert.ErrorContains(t, err, "failed to copy")
}

func TestService_Shell_Interactive(t *testing.T) {
	ml := new(mockLima)
	ml.On("Shell", []string(nil)).Return(nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	require.NoError(t, svc.Shell())
}

func TestService_Shell_WithCommand(t *testing.T) {
	ml := new(mockLima)
	ml.On("Shell", []string{"bash", "-c", "echo hello"}).Return(nil)

	svc := sandbox.NewService(ml, config.Default(), nopLogger)
	require.NoError(t, svc.Shell("bash", "-c", "echo hello"))
}
