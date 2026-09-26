//go:build darwin || linux

package paths

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCertificatePublicationProtectsUnrelatedOutputs(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(root, PublicCertificateName)
	first, second := []byte("public-first"), []byte("public-second")
	require.NoError(t, PublishCertificate(path, first, nil))
	require.NoError(t, PublishCertificate(path, first, nil))
	require.Error(t, PublishCertificate(path, second, nil))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, first, data)
	require.NoError(t, PublishCertificate(path, second, first))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, second, data)
	assertMode(t, path, 0600)
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(path, link))
	require.Error(t, PublishCertificate(link, first, second))
	hardlink := filepath.Join(root, "hardlink")
	require.NoError(t, os.Link(path, hardlink))
	require.Error(t, PublishCertificate(hardlink, first, second))
}

func TestInitialCertificateWriteFailureLeavesDestinationRetryable(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(root, PublicCertificateName)
	certificate := []byte("complete public certificate")
	err := publishNewCertificate(path, certificate, func(file *os.File, data []byte) (int, error) {
		n, err := file.Write(data[:3])
		require.NoError(t, err)
		return n, io.ErrShortWrite
	})
	require.ErrorIs(t, err, io.ErrShortWrite)
	_, err = os.Lstat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, PublishCertificate(path, certificate, nil))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, certificate, data)
	require.NoError(t, CheckCertificateDestination(path, certificate))
}

func TestInitialCertificatePublicationNeverClobbersNewDestination(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(root, PublicCertificateName)
	err := publishNewCertificate(path, []byte("public certificate"), func(file *os.File, data []byte) (int, error) {
		require.NoError(t, os.WriteFile(path, []byte("concurrent output"), 0600))
		return file.Write(data)
	})
	require.ErrorIs(t, err, os.ErrExist)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "concurrent output", string(data))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestAcquireExistingNeverCreatesMissingInstallation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	_, err := AcquireExisting(root)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Lstat(root)
	require.ErrorIs(t, err, os.ErrNotExist)
}
