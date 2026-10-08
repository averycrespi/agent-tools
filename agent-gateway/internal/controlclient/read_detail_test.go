package controlclient

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponseReadCausePreservesHandoffAndPrivacy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte(`{"private":"actual-response-secret"`))
	}))
	defer server.Close()
	client, err := New(server.URL, TransportOptions{})
	require.NoError(t, err)
	_, err = client.Do(t.Context(), Request{Method: http.MethodPost, Path: "/api/v2/principals", Header: http.Header{"Authorization": {"Bearer actual-request-secret"}}, Body: []byte(`{}`)})
	require.ErrorIs(t, err, ErrResponseInvalid)
	require.Equal(t, HandoffPossible, FailureHandoff(err))
	problem := ClassifyRequestError(err, RequestPhaseMutation)
	var human bytes.Buffer
	require.NoError(t, WriteFailure(&human, OutputHuman, problem))
	require.Contains(t, human.String(), "unexpected EOF")
	require.Contains(t, human.String(), server.URL)
	require.NotContains(t, human.String(), "actual-response-secret")
	require.NotContains(t, human.String(), "actual-request-secret")
	machine, err := json.Marshal(problem)
	require.NoError(t, err)
	require.NotContains(t, string(machine), "unexpected EOF")
	require.NotContains(t, string(machine), server.URL)
}

func TestInputFileCauseRemainsLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	_, err := ReadJSONInput(InputOptions{Path: path})
	require.ErrorIs(t, err, ErrInvalidInput)
	problem := NewInputError("The command file input is invalid.", err)
	var human bytes.Buffer
	require.NoError(t, WriteFailure(&human, OutputHuman, problem))
	require.Contains(t, human.String(), path)
	require.Contains(t, human.String(), "no such file")
	machine, err := json.Marshal(problem)
	require.NoError(t, err)
	require.NotContains(t, string(machine), path)
	require.NotContains(t, string(machine), "no such file")
}
