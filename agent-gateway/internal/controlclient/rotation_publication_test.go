package controlclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const rotationTestBearer = "mgw_admin_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestRotationFilePublicationBarrier(t *testing.T) {
	t.Run("fresh file is durable reopened and metadata matched", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "replacement")
		sink, err := PrepareSensitiveSink(SinkOptions{Path: path})
		require.NoError(t, err)
		sink.MarkSubmitted()
		fingerprint := rotationTestFingerprint(rotationTestBearer)
		reopened, err := sink.PublishAdminRotation(rotationTestBearer, fingerprint)
		require.NoError(t, err)
		assert.Equal(t, rotationTestBearer, reopened)
		assert.Equal(t, "owner_only_file", sink.Destination())
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, rotationTestBearer+"\n", string(contents))
		info, err := os.Lstat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		validated, err := AcquireAdminBearer(BearerOptions{FilePath: path})
		require.NoError(t, err)
		assert.Equal(t, rotationTestBearer, validated)
		require.NoError(t, sink.Cleanup())
		assert.FileExists(t, path)
	})

	t.Run("rotation rejects terminal and metadata mismatch without changing ordinary sinks", func(t *testing.T) {
		terminal := new(scriptedTerminal)
		sink, err := PrepareSensitiveSink(SinkOptions{OpenTerminal: func() (io.WriteCloser, error) { return terminal, nil }})
		require.NoError(t, err)
		sink.MarkSubmitted()
		_, err = sink.PublishAdminRotation(rotationTestBearer, rotationTestFingerprint(rotationTestBearer))
		assert.ErrorIs(t, err, ErrSecretLost)
		assert.Empty(t, terminal.String())

		ordinary := new(scriptedTerminal)
		sink, err = PrepareSensitiveSink(SinkOptions{OpenTerminal: func() (io.WriteCloser, error) { return ordinary, nil }})
		require.NoError(t, err)
		sink.MarkSubmitted()
		require.NoError(t, sink.Publish("ordinary-value"))
		assert.Equal(t, "ordinary-value\n", ordinary.String())

		invalidPath := filepath.Join(t.TempDir(), "invalid")
		sink, err = PrepareSensitiveSink(SinkOptions{Path: invalidPath})
		require.NoError(t, err)
		sink.MarkSubmitted()
		_, err = sink.PublishAdminRotation("not-an-admin-bearer", strings.Repeat("0", 16))
		assert.ErrorIs(t, err, ErrSecretLost)
		assert.NoFileExists(t, invalidPath)

		path := filepath.Join(t.TempDir(), "mismatch")
		sink, err = PrepareSensitiveSink(SinkOptions{Path: path})
		require.NoError(t, err)
		sink.MarkSubmitted()
		_, err = sink.PublishAdminRotation(rotationTestBearer, strings.Repeat("0", 16))
		assert.ErrorIs(t, err, ErrSecretLost)
		assert.NoFileExists(t, path)
	})

	t.Run("publication order and every I/O boundary fail closed", func(t *testing.T) {
		expectedOrder := []string{"lstat", "write", "file_sync", "lstat", "file_close", "lstat", "dir_open", "dir_sync", "dir_close", "lstat", "reopen", "lstat"}
		sink, file, directory, operations := newScriptedRotationSink(t)
		reopened, err := sink.PublishAdminRotation(rotationTestBearer, rotationTestFingerprint(rotationTestBearer))
		require.NoError(t, err)
		assert.Equal(t, rotationTestBearer, reopened)
		assert.Equal(t, expectedOrder, *operations)
		assert.Equal(t, rotationTestBearer+"\n", file.String())
		assert.True(t, directory.closed)

		for _, failure := range []string{"write", "short_write", "file_sync", "file_close", "dir_open", "dir_sync", "dir_close", "reopen", "reopen_mismatch"} {
			t.Run(failure, func(t *testing.T) {
				sink, file, directory, _ := newScriptedRotationSink(t)
				fault := errors.New("private fault detail")
				switch failure {
				case "write":
					file.writeErr = fault
				case "short_write":
					file.shortWrite = true
				case "file_sync":
					file.syncErr = fault
				case "file_close":
					file.closeErr = fault
				case "dir_open":
					sink.ops.openDirectory = func(string) (sensitiveDirectory, error) { return nil, fault }
				case "dir_sync":
					directory.syncErr = fault
				case "dir_close":
					directory.closeErr = fault
				case "reopen":
					sink.ops.readBearerFile = func(string, int) (string, error) { return "", fault }
				case "reopen_mismatch":
					sink.ops.readBearerFile = func(string, int) (string, error) {
						return "mgw_admin_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", nil
					}
				}
				_, err := sink.PublishAdminRotation(rotationTestBearer, rotationTestFingerprint(rotationTestBearer))
				assert.ErrorIs(t, err, ErrSecretLost)
				assert.NotContains(t, err.Error(), "private fault detail")
				assert.NotContains(t, err.Error(), rotationTestBearer)
				assert.NoFileExists(t, sink.path)
			})
		}
	})

	t.Run("path races at every revalidation boundary preserve replacements", func(t *testing.T) {
		for failedCheck := 1; failedCheck <= 5; failedCheck++ {
			t.Run(string(rune('0'+failedCheck)), func(t *testing.T) {
				sink, _, _, _ := newScriptedRotationSink(t)
				replacementPath := filepath.Join(t.TempDir(), "replacement")
				require.NoError(t, os.WriteFile(replacementPath, []byte("replacement"), 0o600))
				replacementInfo, err := os.Lstat(replacementPath)
				require.NoError(t, err)
				checks := 0
				removals := 0
				sink.ops.lstat = func(string) (os.FileInfo, error) {
					checks++
					if checks >= failedCheck {
						return replacementInfo, nil
					}
					return sink.fileInfo, nil
				}
				sink.ops.remove = func(string) error { removals++; return nil }
				_, err = sink.PublishAdminRotation(rotationTestBearer, rotationTestFingerprint(rotationTestBearer))
				assert.ErrorIs(t, err, ErrSecretLost)
				assert.Zero(t, removals, "a raced replacement must never be removed")
			})
		}
	})
}

func newScriptedRotationSink(t *testing.T) (*PreparedSink, *scriptedRotationFile, *scriptedRotationDirectory, *[]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rotation")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	operations := make([]string, 0, 12)
	file := &scriptedRotationFile{operations: &operations}
	directory := &scriptedRotationDirectory{operations: &operations}
	ops := defaultSensitiveSinkOps()
	ops.lstat = func(path string) (os.FileInfo, error) {
		operations = append(operations, "lstat")
		return os.Lstat(path)
	}
	ops.remove = os.Remove
	ops.openDirectory = func(string) (sensitiveDirectory, error) {
		operations = append(operations, "dir_open")
		return directory, nil
	}
	ops.readBearerFile = func(string, int) (string, error) {
		operations = append(operations, "reopen")
		return rotationTestBearer, nil
	}
	sink := &PreparedSink{output: file, file: file, fileInfo: info, path: path, destination: "owner_only_file", submitted: true, ops: ops}
	return sink, file, directory, &operations
}

type scriptedRotationFile struct {
	bytes.Buffer
	operations *[]string
	writeErr   error
	syncErr    error
	closeErr   error
	shortWrite bool
}

func (file *scriptedRotationFile) WriteString(contents string) (int, error) {
	return file.Write([]byte(contents))
}

func (file *scriptedRotationFile) Write(contents []byte) (int, error) {
	*file.operations = append(*file.operations, "write")
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	if file.shortWrite {
		return len(contents) - 1, nil
	}
	return file.Buffer.Write(contents)
}

func (file *scriptedRotationFile) Sync() error {
	*file.operations = append(*file.operations, "file_sync")
	return file.syncErr
}

func (file *scriptedRotationFile) Close() error {
	*file.operations = append(*file.operations, "file_close")
	return file.closeErr
}

type scriptedRotationDirectory struct {
	operations *[]string
	syncErr    error
	closeErr   error
	closed     bool
}

func (directory *scriptedRotationDirectory) Sync() error {
	*directory.operations = append(*directory.operations, "dir_sync")
	return directory.syncErr
}

func (directory *scriptedRotationDirectory) Close() error {
	*directory.operations = append(*directory.operations, "dir_close")
	directory.closed = true
	return directory.closeErr
}

func rotationTestFingerprint(bearer string) string {
	verifier := sha256.Sum256(append([]byte("mcp-gateway/admin-verifier/v1\x00"), bearer...))
	digest := sha256.Sum256(append([]byte("mcp-gateway/admin-fingerprint/v1\x00"), verifier[:]...))
	return hex.EncodeToString(digest[:8])
}
