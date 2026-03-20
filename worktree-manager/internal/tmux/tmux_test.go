package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-manager/internal/exec"
)

type mockRunner struct {
	mock.Mock
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) {
	callArgs := m.Called(name, args)
	return callArgs.Get(0).([]byte), callArgs.Error(1)
}

func (m *mockRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	callArgs := m.Called(dir, name, args)
	return callArgs.Get(0).([]byte), callArgs.Error(1)
}

func (m *mockRunner) RunInteractive(name string, args ...string) error {
	callArgs := m.Called(name, args)
	return callArgs.Error(0)
}

var _ exec.Runner = (*mockRunner)(nil)

func TestClient_SessionExists_True(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "has-session", "-t", "mysess"}).Return([]byte(""), nil)

	client := NewClient(r)
	assert.True(t, client.SessionExists("mysess"))
}

func TestClient_SessionExists_False(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "has-session", "-t", "nosess"}).Return([]byte(""), assert.AnError)

	client := NewClient(r)
	assert.False(t, client.SessionExists("nosess"))
}

func TestClient_CreateSession(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "new-session", "-d", "-s", "sess", "-n", "main"}).Return([]byte(""), nil)

	client := NewClient(r)
	err := client.CreateSession("sess", "main")

	require.NoError(t, err)
	r.AssertExpectations(t)
}

func TestClient_CreateWindow(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "new-window", "-t", "sess", "-n", "win", "-c", "/dir", "-d"}).Return([]byte(""), nil)

	client := NewClient(r)
	err := client.CreateWindow("sess", "win", "/dir")

	require.NoError(t, err)
	r.AssertExpectations(t)
}

func TestClient_KillWindow(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "kill-window", "-t", "sess:win"}).Return([]byte(""), nil)

	client := NewClient(r)
	err := client.KillWindow("sess", "win")

	require.NoError(t, err)
}

func TestClient_ListWindows(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "list-windows", "-t", "sess", "-F", "#{window_name}"}).Return([]byte("main\nfeature\n"), nil)

	client := NewClient(r)
	windows, err := client.ListWindows("sess")

	require.NoError(t, err)
	assert.Equal(t, []string{"main", "feature"}, windows)
}

func TestClient_ListWindows_Empty(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "list-windows", "-t", "sess", "-F", "#{window_name}"}).Return([]byte(""), nil)

	client := NewClient(r)
	windows, err := client.ListWindows("sess")

	require.NoError(t, err)
	assert.Nil(t, windows)
}

func TestClient_WindowExists_Direct(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "list-windows", "-t", "sess", "-F", "#{window_name}"}).Return([]byte("main\nfeat\n"), nil)

	client := NewClient(r)
	assert.True(t, client.WindowExists("sess", "feat"))
}

func TestClient_WindowExists_NotFound(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "list-windows", "-t", "sess", "-F", "#{window_name}"}).Return([]byte("main\n"), nil)

	client := NewClient(r)
	assert.False(t, client.WindowExists("sess", "nope"))
}

func TestClient_SendKeys(t *testing.T) {
	r := new(mockRunner)
	r.On("Run", "tmux", []string{"-L", SocketName, "send-keys", "-t", "sess:win", "echo hi", "C-m"}).Return([]byte(""), nil)

	client := NewClient(r)
	err := client.SendKeys("sess", "win", "echo hi")

	require.NoError(t, err)
}

func TestClient_Attach(t *testing.T) {
	r := new(mockRunner)
	r.On("RunInteractive", "tmux", []string{"-L", SocketName, "attach-session", "-t", "sess"}).Return(nil)

	client := NewClient(r)
	err := client.Attach("sess")

	require.NoError(t, err)
}

func TestClient_AttachToWindow(t *testing.T) {
	r := new(mockRunner)
	r.On("RunInteractive", "tmux", []string{"-L", SocketName, "attach-session", "-t", "sess:win"}).Return(nil)

	client := NewClient(r)
	err := client.AttachToWindow("sess", "win")

	require.NoError(t, err)
}

func TestInsideWtSocket(t *testing.T) {
	tests := []struct {
		name   string
		tmux   string
		expect bool
	}{
		{"empty", "", false},
		{"default socket", "/tmp/tmux-501/default,1234,0", false},
		{"wt socket", "/tmp/tmux-501/wt,1234,0", true},
		{"private tmp wt", "/private/tmp/tmux-501/wt,5678,1", true},
		{"similar name", "/tmp/tmux-501/wt-other,1234,0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, insideWtSocket(tt.tmux))
		})
	}
}
