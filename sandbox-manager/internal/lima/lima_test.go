package lima_test

import (
	"testing"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/lima"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockRunner struct {
	mock.Mock
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) {
	called := m.Called(append([]interface{}{name}, toInterfaceSlice(args)...)...)
	return called.Get(0).([]byte), called.Error(1)
}

func (m *mockRunner) RunInteractive(name string, args ...string) error {
	called := m.Called(append([]interface{}{name}, toInterfaceSlice(args)...)...)
	return called.Error(0)
}

func toInterfaceSlice(s []string) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func TestClient_Status_Running(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "limactl", "list", "--json").Return(
		[]byte(`{"name":"sb","status":"Running"}`+"\n"),
		nil,
	)
	c := lima.NewClient(r)
	status, err := c.Status()
	require.NoError(t, err)
	assert.Equal(t, lima.StatusRunning, status)
	r.AssertExpectations(t)
}

func TestClient_Status_NotCreated(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "limactl", "list", "--json").Return([]byte(""), nil)
	c := lima.NewClient(r)
	status, err := c.Status()
	require.NoError(t, err)
	assert.Equal(t, lima.StatusNotCreated, status)
	r.AssertExpectations(t)
}

func TestClient_Create(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "limactl", "start", "--name=sb", "/tmp/lima.yaml").Return([]byte(""), nil)
	c := lima.NewClient(r)
	require.NoError(t, c.Create("/tmp/lima.yaml"))
	r.AssertExpectations(t)
}

func TestClient_Start(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "limactl", "start", "sb").Return([]byte(""), nil)
	c := lima.NewClient(r)
	require.NoError(t, c.Start())
	r.AssertExpectations(t)
}

func TestClient_Stop(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "limactl", "stop", "sb").Return([]byte(""), nil)
	c := lima.NewClient(r)
	require.NoError(t, c.Stop())
	r.AssertExpectations(t)
}

func TestClient_Delete(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "limactl", "delete", "--force", "sb").Return([]byte(""), nil)
	c := lima.NewClient(r)
	require.NoError(t, c.Delete())
	r.AssertExpectations(t)
}

func TestClient_Copy(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "limactl", "cp", "/host/file", "sb:/guest/file").Return([]byte(""), nil)
	c := lima.NewClient(r)
	require.NoError(t, c.Copy("/host/file", "/guest/file", false))
	r.AssertExpectations(t)
}

func TestClient_Copy_Recursive(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "limactl", "cp", "-r", "/host/dir", "sb:/guest/dir").Return([]byte(""), nil)
	c := lima.NewClient(r)
	require.NoError(t, c.Copy("/host/dir", "/guest/dir", true))
	r.AssertExpectations(t)
}

func TestClient_Exec(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "limactl", "shell", "--workdir", "/", "sb", "--", "mkdir", "-p", "/tmp/test").Return([]byte(""), nil)
	c := lima.NewClient(r)
	_, err := c.Exec("mkdir", "-p", "/tmp/test")
	require.NoError(t, err)
	r.AssertExpectations(t)
}

func TestClient_Shell_Interactive(t *testing.T) {
	r := new(mockRunner)
	r.On("RunInteractive", "limactl", "shell", "sb").Return(nil)
	c := lima.NewClient(r)
	require.NoError(t, c.Shell())
	r.AssertExpectations(t)
}

func TestClient_Shell_WithCommand(t *testing.T) {
	r := new(mockRunner)
	r.On("RunInteractive", "limactl", "shell", "sb", "--", "bash", "-c", "echo hello").Return(nil)
	c := lima.NewClient(r)
	require.NoError(t, c.Shell("bash", "-c", "echo hello"))
	r.AssertExpectations(t)
}
