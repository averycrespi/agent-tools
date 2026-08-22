package testutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFakeClockAdvancesDeterministically(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	require.Equal(t, start, clock.Now())
	clock.Advance(90 * time.Second)
	require.Equal(t, start.Add(90*time.Second), clock.Now())
	clock.Set(start.Add(2 * time.Hour))
	require.Equal(t, start.Add(2*time.Hour), clock.Now())
}

func TestFakeEntropyReturnsOnlyTheConfiguredSequence(t *testing.T) {
	t.Parallel()

	entropy := NewFakeEntropy([]byte{1, 2, 3, 4})
	first := make([]byte, 3)
	count, err := io.ReadFull(entropy, first)
	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.Equal(t, []byte{1, 2, 3}, first)
	require.Equal(t, 1, entropy.Remaining())

	last := make([]byte, 2)
	count, err = entropy.Read(last)
	require.Equal(t, 1, count)
	require.NoError(t, err)
	require.Equal(t, byte(4), last[0])

	count, err = entropy.Read(last)
	require.Zero(t, count)
	require.ErrorIs(t, err, io.EOF)
}

func TestOwnerOnlyDataRootRejectsUnsafeFilesystemState(t *testing.T) {
	t.Parallel()

	root := NewOwnerOnlyDataRoot(t)
	require.NoError(t, ValidateOwnerOnlyDataRoot(root))

	require.NoError(t, os.Chmod(root, 0o755))
	err := ValidateOwnerOnlyDataRoot(root)
	require.ErrorContains(t, err, "permissions")

	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "data-link")
	require.NoError(t, os.Symlink(target, link))
	err = ValidateOwnerOnlyDataRoot(link)
	require.ErrorContains(t, err, "symlink")

	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	err = ValidateOwnerOnlyDataRoot(file)
	require.ErrorContains(t, err, "directory")
}

func TestCanaryScannerFindsEveryCommonSinkWithoutEchoingTheSecret(t *testing.T) {
	t.Parallel()

	const canary = "mgw_admin_intentional_canary"
	scanner, err := NewCanaryScanner([]byte(canary))
	require.NoError(t, err)

	for _, sink := range []string{
		"stdout", "stderr", "argv", "logs", "data_file", "sqlite_fixture", "backup_fixture", "http_fixture", "event_fixture",
	} {
		err := scanner.Scan(sink, strings.NewReader("prefix "+canary+" suffix"))
		var leak *LeakError
		require.ErrorAs(t, err, &leak, sink)
		require.Equal(t, sink, leak.Sink)
		require.NotContains(t, err.Error(), canary)
	}

	require.NoError(t, scanner.Scan("clean", strings.NewReader("metadata only")))
}

func TestCanaryScannerDetectsCanaryAcrossReadBoundaries(t *testing.T) {
	t.Parallel()

	scanner, err := NewCanaryScanner([]byte("secret-canary"))
	require.NoError(t, err)

	err = scanner.Scan("chunked", &chunkReader{chunks: [][]byte{[]byte("prefix secret-"), []byte("can"), []byte("ary suffix")}})
	require.Error(t, err)
}

func TestCanaryScannerRejectsEmptyCanary(t *testing.T) {
	t.Parallel()

	_, err := NewCanaryScanner(nil)
	require.ErrorContains(t, err, "empty")
}

func TestBinaryRunnerCapturesSeparateRealProcessResults(t *testing.T) {
	t.Parallel()

	runner, err := NewBinaryRunner(2*time.Second, 1024)
	require.NoError(t, err)

	result, runErr := runner.Run(context.Background(), "sh", "-c", "printf stdout; printf stderr >&2; exit 7")
	require.Error(t, runErr)
	require.Equal(t, 7, result.ExitCode)
	require.Equal(t, []byte("stdout"), result.Stdout)
	require.Equal(t, []byte("stderr"), result.Stderr)
	require.False(t, result.StdoutTruncated)
	require.False(t, result.StderrTruncated)
}

func TestBinaryRunnerBoundsEachOutputStream(t *testing.T) {
	t.Parallel()

	runner, err := NewBinaryRunner(2*time.Second, 8)
	require.NoError(t, err)

	result, runErr := runner.Run(context.Background(), "sh", "-c", "printf 1234567890; printf abcdefghij >&2")
	require.NoError(t, runErr)
	require.Equal(t, 0, result.ExitCode)
	require.Equal(t, []byte("12345678"), result.Stdout)
	require.Equal(t, []byte("abcdefgh"), result.Stderr)
	require.True(t, result.StdoutTruncated)
	require.True(t, result.StderrTruncated)
}

func TestBinaryRunnerAppliesItsFiniteDeadline(t *testing.T) {
	t.Parallel()

	runner, err := NewBinaryRunner(100*time.Millisecond, 1024)
	require.NoError(t, err)

	result, runErr := runner.Run(context.Background(), "sh", "-c", "while :; do :; done")
	require.ErrorIs(t, runErr, context.DeadlineExceeded)
	require.Equal(t, -1, result.ExitCode)
}

func TestBinaryRunnerRejectsUnboundedConfiguration(t *testing.T) {
	t.Parallel()

	_, err := NewBinaryRunner(0, 1024)
	require.ErrorContains(t, err, "timeout")
	_, err = NewBinaryRunner(time.Second, 0)
	require.ErrorContains(t, err, "output")
}

type chunkReader struct {
	chunks [][]byte
}

func (reader *chunkReader) Read(buffer []byte) (int, error) {
	if len(reader.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := reader.chunks[0]
	reader.chunks = reader.chunks[1:]
	return copy(buffer, chunk), nil
}

func TestBoundedWriterReportsTruncationWithoutShortWrites(t *testing.T) {
	t.Parallel()

	writer := newBoundedWriter(4)
	count, err := writer.Write([]byte("abcdef"))
	require.NoError(t, err)
	require.Equal(t, 6, count)
	require.Equal(t, []byte("abcd"), writer.Bytes())
	require.True(t, writer.Truncated())

	count, err = writer.Write([]byte("more"))
	require.NoError(t, err)
	require.Equal(t, 4, count)
	require.True(t, bytes.Equal([]byte("abcd"), writer.Bytes()))
}

func TestLeakErrorSupportsErrorsAs(t *testing.T) {
	t.Parallel()

	original := &LeakError{Sink: "stdout"}
	wrapped := errors.Join(errors.New("scan failed"), original)
	var actual *LeakError
	require.ErrorAs(t, wrapped, &actual)
	require.Equal(t, original, actual)
}
