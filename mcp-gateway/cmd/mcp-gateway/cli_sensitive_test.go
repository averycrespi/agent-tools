package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLISensitiveSinks(t *testing.T) {
	for _, spec := range onlineCommandSpecs() {
		joined := strings.Join(spec.Path, " ")
		if contains(spec.Flags, "secret-output") {
			assert.Contains(t, []string{"admin credential create", "admin credential rotate", "principal credential issue", "principal credential rotate"}, joined)
		}
		if contains(spec.Flags, "yes") {
			assert.NotContains(t, []string{"status", "server get", "invocation get"}, joined)
		}
	}

	path := filepath.Join(t.TempDir(), "prepared")
	prompt := &commandConfirmation{answer: true}
	sink, failure := prepareOnlineSensitiveAction(
		&onlineOptions{secretOutput: path},
		"This replaces current authority.",
		prompt,
		nil,
	)
	require.Nil(t, failure)
	require.NotNil(t, sink)
	assert.Equal(t, "This replaces current authority.", prompt.consequence)
	require.NoError(t, sink.Cleanup())

	canary := "CLI_SECRET_" + strings.Repeat("s", 32)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command := newRootCmd()
	command.SetOut(stdout)
	command.SetErr(stderr)
	err := writeOnlineFailure(command, string(controlclient.OutputJSON), controlclient.NewSecretSinkError("The one-time value could not be published."))
	require.Error(t, err)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Empty(t, stdout.String())
	assert.NotContains(t, stderr.String(), canary)
	assert.JSONEq(t, `{"status":null,"code":"client_secret_sink_unavailable","title":"The one-time value could not be published.","exit_code":2,"uncertain":false}`, stderr.String())

	_, failure = prepareOnlineSensitiveAction(
		&onlineOptions{yes: true},
		"This revokes authority.",
		&commandConfirmation{err: errors.New("must not be called")},
		func() (io.WriteCloser, error) { return nil, errors.New("terminal unavailable") },
	)
	require.NotNil(t, failure)
	assert.Equal(t, "client_secret_sink_unavailable", failure.Code)
	assert.Contains(t, failure.Title, "No credential request was submitted")
	assert.Contains(t, failure.Title, "controlling terminal")
}

func TestCredentialSuccessHumanOutputConfirmsOneTimePublication(t *testing.T) {
	credential := contract.AdminCredential{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Fingerprint: "fingerprint", Status: contract.CredentialActive, Revision: "1", CreatedAt: "2026-08-30T00:00:00Z"}
	for _, test := range []struct {
		path     string
		expected string
	}{
		{path: filepath.Join(t.TempDir(), "bearer"), expected: "owner-only file"},
		{expected: "controlling terminal"},
	} {
		command := newRootCmd()
		stdout := new(bytes.Buffer)
		command.SetOut(stdout)
		require.NoError(t, writeAdminCredentialSuccess(command, &onlineOptions{output: "human", secretOutput: test.path}, credential))
		assert.Contains(t, stdout.String(), test.expected)
		assert.Contains(t, stdout.String(), "cannot be shown again")
		assert.Contains(t, stdout.String(), "not included in command output")

		stdout.Reset()
		require.NoError(t, writePrincipalMetadataSuccess(command, &onlineOptions{output: "human", secretOutput: test.path}, contract.Principal{}, true))
		assert.Contains(t, stdout.String(), test.expected)
		assert.Contains(t, stdout.String(), "cannot be shown again")
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type commandConfirmation struct {
	answer      bool
	err         error
	consequence string
}

func (prompt *commandConfirmation) Confirm(consequence string) (bool, error) {
	prompt.consequence = consequence
	return prompt.answer, prompt.err
}
