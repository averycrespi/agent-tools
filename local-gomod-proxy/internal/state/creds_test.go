package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrGenerateCredentials_GeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	creds, err := LoadOrGenerateCredentials(dir)
	require.NoError(t, err)
	assert.Equal(t, "x", creds.Username)
	assert.NotEmpty(t, creds.Password)
	// base64url of 32 bytes is 43 chars (no padding).
	assert.Len(t, creds.Password, 43)

	path := filepath.Join(dir, "credentials")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "x:"+creds.Password+"\n", string(raw))
}

func TestLoadOrGenerateCredentials_ReusesWhenPresent(t *testing.T) {
	dir := t.TempDir()
	c1, err := LoadOrGenerateCredentials(dir)
	require.NoError(t, err)
	c2, err := LoadOrGenerateCredentials(dir)
	require.NoError(t, err)
	assert.Equal(t, c1, c2)
}

func TestLoadOrGenerateCredentials_MalformedFailsHard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	require.NoError(t, os.WriteFile(path, []byte("not-a-valid-line"), 0o600))

	_, err := LoadOrGenerateCredentials(dir)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "malformed")

	// File must not have been clobbered.
	raw, _ := os.ReadFile(path)
	assert.Equal(t, "not-a-valid-line", string(raw))
}

func TestLoadOrGenerateCredentials_WrongUsernameFailsHard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	require.NoError(t, os.WriteFile(path, []byte("admin:hunter2\n"), 0o600))

	_, err := LoadOrGenerateCredentials(dir)
	require.Error(t, err)
}

func TestLoadOrGenerateCredentials_GeneratesDifferentPasswords(t *testing.T) {
	c1, err := LoadOrGenerateCredentials(t.TempDir())
	require.NoError(t, err)
	c2, err := LoadOrGenerateCredentials(t.TempDir())
	require.NoError(t, err)
	assert.NotEqual(t, c1.Password, c2.Password)
}
