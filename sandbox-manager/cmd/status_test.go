package cmd

import (
	"bytes"
	"testing"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/lima"
	"github.com/stretchr/testify/require"
)

type fakeStatusService struct{ status lima.Status }

func (s fakeStatusService) Status() (lima.Status, error) { return s.status, nil }

func TestRunStatusCommandWritesHumanStatus(t *testing.T) {
	var out bytes.Buffer

	err := runStatusCommand(fakeStatusService{status: lima.StatusNotCreated}, false, &out)

	require.NoError(t, err)
	require.Equal(t, "not created\n", out.String())
}

func TestRunStatusCommandWritesJSONStatus(t *testing.T) {
	var out bytes.Buffer

	err := runStatusCommand(fakeStatusService{status: lima.StatusNotCreated}, true, &out)

	require.NoError(t, err)
	require.JSONEq(t, `{"status":"not_created"}`, out.String())
}

func TestStatusCommandDefinesJSONFlag(t *testing.T) {
	flag := statusCmd.Flags().Lookup("json")
	require.NotNil(t, flag)
	require.Empty(t, flag.Shorthand)
}
