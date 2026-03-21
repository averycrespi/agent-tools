package sandbox_test

import (
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderTemplate_ContainsImage(t *testing.T) {
	params := sandbox.TemplateParams{
		Image:    "ubuntu-24.04",
		CPUs:     4,
		Memory:   "4GiB",
		Disk:     "100GiB",
		Username: "testuser",
		UID:      501,

		HomeDir: "/Users/testuser",
		Mounts:  []string{"/Users/testuser/work"},
	}
	out, err := sandbox.RenderTemplate(params)
	require.NoError(t, err)
	assert.Contains(t, out, "ubuntu-24.04")
	assert.Contains(t, out, "testuser")
	assert.Contains(t, out, "/Users/testuser/work")
}

func TestRenderTemplate_NoMounts(t *testing.T) {
	params := sandbox.TemplateParams{
		Image:    "ubuntu-24.04",
		CPUs:     2,
		Memory:   "2GiB",
		Disk:     "50GiB",
		Username: "testuser",
		UID:      501,

		HomeDir: "/Users/testuser",
		Mounts:  []string{},
	}
	out, err := sandbox.RenderTemplate(params)
	require.NoError(t, err)
	assert.NotContains(t, out, "mountPoint")
}

func TestRenderTemplate_MultipleMounts(t *testing.T) {
	params := sandbox.TemplateParams{
		Image:    "ubuntu-24.04",
		CPUs:     4,
		Memory:   "4GiB",
		Disk:     "100GiB",
		Username: "testuser",
		UID:      501,

		HomeDir: "/Users/testuser",
		Mounts:  []string{"/Users/testuser/work", "/Users/testuser/projects"},
	}
	out, err := sandbox.RenderTemplate(params)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(out, "writable: true"))
}

func TestHostTemplateParams(t *testing.T) {
	params, err := sandbox.HostTemplateParams()
	require.NoError(t, err)
	assert.NotEmpty(t, params.Username)
	assert.NotZero(t, params.UID)
	assert.NotEmpty(t, params.HomeDir)
}
