package controlclient

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6CLISensitiveSinks(t *testing.T) {
	t.Run("confirmation is consequence specific and closed", func(t *testing.T) {
		prompt := &fakeConfirmation{answer: true}
		require.NoError(t, RequireConfirmation(ConfirmationOptions{Consequence: "This permanently deletes server alpha.", Prompt: prompt}))
		assert.Equal(t, "This permanently deletes server alpha.", prompt.consequence)
		calls := prompt.calls
		require.NoError(t, RequireConfirmation(ConfirmationOptions{Yes: true, Consequence: "This revokes authority.", Prompt: prompt}))
		assert.Equal(t, calls, prompt.calls, "--yes must not touch a terminal")

		prompt.answer = false
		err := RequireConfirmation(ConfirmationOptions{Consequence: "This interrupts authority.", Prompt: prompt})
		assert.ErrorIs(t, err, ErrConfirmationRequired)
		prompt.err = errors.New("terminal unavailable with private detail")
		err = RequireConfirmation(ConfirmationOptions{Consequence: "This interrupts authority.", Prompt: prompt})
		assert.ErrorIs(t, err, ErrConfirmationRequired)
		assert.NotContains(t, err.Error(), "private detail")
	})

	t.Run("file sink is prepared before submission", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "secret")
		sink, err := PrepareSensitiveSink(SinkOptions{Path: path})
		require.NoError(t, err)
		info, err := os.Lstat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		assert.Equal(t, "owner_only_file", sink.Destination())
		require.NoError(t, sink.Cleanup())
		_, err = os.Lstat(path)
		assert.ErrorIs(t, err, os.ErrNotExist)

		sink, err = PrepareSensitiveSink(SinkOptions{Path: path})
		require.NoError(t, err)
		sink.MarkSubmitted()
		canary := "SECRET_CANARY_" + strings.Repeat("x", 32)
		require.NoError(t, sink.Publish(canary))
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, canary+"\n", string(contents))
		assert.NotContains(t, sink.Destination(), canary)

		_, err = PrepareSensitiveSink(SinkOptions{Path: path})
		assert.ErrorIs(t, err, ErrSecretSinkUnavailable)
		target := filepath.Join(root, "target")
		require.NoError(t, os.WriteFile(target, nil, 0o600))
		link := filepath.Join(root, "link")
		require.NoError(t, os.Symlink(target, link))
		_, err = PrepareSensitiveSink(SinkOptions{Path: link})
		assert.ErrorIs(t, err, ErrSecretSinkUnavailable)

		racePath := filepath.Join(root, "raced")
		displaced := filepath.Join(root, "displaced")
		sink, err = PrepareSensitiveSink(SinkOptions{Path: racePath})
		require.NoError(t, err)
		require.NoError(t, os.Rename(racePath, displaced))
		require.NoError(t, os.WriteFile(racePath, []byte("replacement"), 0o600))
		sink.MarkSubmitted()
		err = sink.Publish(canary)
		assert.ErrorIs(t, err, ErrSecretLost)
		replacement, readErr := os.ReadFile(racePath)
		require.NoError(t, readErr)
		assert.Equal(t, "replacement", string(replacement), "cleanup must not remove or overwrite a raced replacement")
		displacedContents, readErr := os.ReadFile(displaced)
		require.NoError(t, readErr)
		assert.Empty(t, displacedContents, "publication must revalidate the prepared path before writing")
	})

	t.Run("terminal publication is preflighted and lost values are explicit", func(t *testing.T) {
		terminal := new(scriptedTerminal)
		sink, err := PrepareSensitiveSink(SinkOptions{OpenTerminal: func() (io.WriteCloser, error) { return terminal, nil }})
		require.NoError(t, err)
		assert.Equal(t, "controlling_terminal", sink.Destination())
		sink.MarkSubmitted()
		canary := "OAUTH_OR_SECRET_" + strings.Repeat("z", 32)
		require.NoError(t, sink.Publish(canary))
		assert.Equal(t, canary+"\n", terminal.String())

		failed := &scriptedTerminal{writeErr: errors.New("private write detail")}
		sink, err = PrepareSensitiveSink(SinkOptions{OpenTerminal: func() (io.WriteCloser, error) { return failed, nil }})
		require.NoError(t, err)
		sink.MarkSubmitted()
		err = sink.Publish(canary)
		assert.ErrorIs(t, err, ErrSecretLost)
		assert.NotContains(t, err.Error(), canary)
		assert.NotContains(t, err.Error(), "private write detail")
		failure := ClassifyClientError(err)
		assert.Equal(t, "client_secret_sink_unavailable", failure.Code)
		assert.Equal(t, 2, failure.ExitCode())

		_, err = PrepareSensitiveSink(SinkOptions{OpenTerminal: func() (io.WriteCloser, error) { return nil, errors.New("private open detail") }})
		assert.ErrorIs(t, err, ErrSecretSinkUnavailable)
		assert.NotContains(t, err.Error(), "private open detail")
	})

	t.Run("sensitive material never enters ordinary output or process inputs", func(t *testing.T) {
		canary := "PROCESS_CANARY_" + strings.Repeat("q", 32)
		terminal := new(scriptedTerminal)
		sink, err := PrepareSensitiveSink(SinkOptions{OpenTerminal: func() (io.WriteCloser, error) { return terminal, nil }})
		require.NoError(t, err)
		sink.MarkSubmitted()
		require.NoError(t, sink.Publish(canary))
		var stdout, stderr bytes.Buffer
		require.NoError(t, WriteSuccess(&stdout, OutputJSON, []byte(`{"id":"safe"}`), Table{}))
		require.NoError(t, WriteFailure(&stderr, OutputJSON, NewSecretSinkError("The one-time value could not be published.")))
		assert.NotContains(t, stdout.String()+stderr.String()+strings.Join(os.Args, " ")+strings.Join(os.Environ(), " "), canary)
	})
}

type fakeConfirmation struct {
	answer      bool
	err         error
	consequence string
	calls       int
}

func (prompt *fakeConfirmation) Confirm(consequence string) (bool, error) {
	prompt.calls++
	prompt.consequence = consequence
	return prompt.answer, prompt.err
}

type scriptedTerminal struct {
	buffer   bytes.Buffer
	writeErr error
	closeErr error
}

func (terminal *scriptedTerminal) Write(contents []byte) (int, error) {
	if terminal.writeErr != nil {
		return 0, terminal.writeErr
	}
	return terminal.buffer.Write(contents)
}

func (terminal *scriptedTerminal) String() string { return terminal.buffer.String() }
func (terminal *scriptedTerminal) Close() error   { return terminal.closeErr }
