package sandbox_test

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/config"
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

func (m *mockLima) Copy(localPath, guestPath string, recursive bool) error {
	return m.Called(localPath, guestPath, recursive).Error(0)
}

func (m *mockLima) Exec(args ...string) ([]byte, error) {
	called := m.Called(args)
	return called.Get(0).([]byte), called.Error(1)
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
	ml.On("Exec", []string{"mkdir", "-p", filepath.Dir(tmpFile)}).Return([]byte(""), nil)
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
	ml.On("Exec", []string{"mkdir", "-p", tmpDir}).Return([]byte(""), nil)
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

func TestService_Create_AlreadyRunning(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), ".zshrc")
	require.NoError(t, os.WriteFile(tmpFile, []byte(""), 0o644))
	resolvedFile, err := filepath.EvalSymlinks(tmpFile)
	require.NoError(t, err)

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Exec", []string{"mkdir", "-p", filepath.Dir(tmpFile)}).Return([]byte(""), nil)
	ml.On("Copy", resolvedFile, tmpFile, false).Return(nil)

	cfg := config.Default()
	cfg.CopyPaths = []string{tmpFile}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Create())
	ml.AssertNotCalled(t, "Start")
	ml.AssertCalled(t, "Copy", resolvedFile, tmpFile, false)
}

func TestService_Create_Stopped(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusStopped, nil).Once()
	ml.On("Start").Return(nil)
	ml.On("Status").Return(lima.StatusRunning, nil)

	cfg := config.Default()
	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Create())
	ml.AssertCalled(t, "Start")
}

func TestService_Provision_Scripts(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Copy", "/home/user/setup.sh", "/tmp/sb-provision-script", false).Return(nil)
	ml.On("Exec", []string{"chmod", "+x", "/tmp/sb-provision-script"}).Return([]byte(""), nil)
	ml.On("Exec", []string{"/tmp/sb-provision-script"}).Return([]byte(""), nil)
	ml.On("Exec", []string{"rm", "-f", "/tmp/sb-provision-script"}).Return([]byte(""), nil)

	cfg := config.Default()
	cfg.Scripts = []string{"/home/user/setup.sh"}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	require.NoError(t, svc.Provision())
	ml.AssertCalled(t, "Copy", "/home/user/setup.sh", "/tmp/sb-provision-script", false)
	ml.AssertCalled(t, "Exec", []string{"chmod", "+x", "/tmp/sb-provision-script"})
	ml.AssertCalled(t, "Exec", []string{"/tmp/sb-provision-script"})
	ml.AssertCalled(t, "Exec", []string{"rm", "-f", "/tmp/sb-provision-script"})
}

func TestService_Provision_ScriptExecError(t *testing.T) {
	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Copy", "/home/user/setup.sh", "/tmp/sb-provision-script", false).Return(nil)
	ml.On("Exec", []string{"chmod", "+x", "/tmp/sb-provision-script"}).Return([]byte(""), nil)
	ml.On("Exec", []string{"/tmp/sb-provision-script"}).Return([]byte(""), fmt.Errorf("exit code 1"))

	cfg := config.Default()
	cfg.Scripts = []string{"/home/user/setup.sh"}

	svc := sandbox.NewService(ml, cfg, nopLogger)
	err := svc.Provision()
	assert.ErrorContains(t, err, "failed to run script")
}

func TestService_Provision_CopyError(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), ".zshrc")
	require.NoError(t, os.WriteFile(tmpFile, []byte(""), 0o644))
	resolvedFile, err := filepath.EvalSymlinks(tmpFile)
	require.NoError(t, err)

	ml := new(mockLima)
	ml.On("Status").Return(lima.StatusRunning, nil)
	ml.On("Exec", []string{"mkdir", "-p", filepath.Dir(tmpFile)}).Return([]byte(""), nil)
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
