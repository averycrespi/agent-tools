package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLocalTransportFailureUsefulWithoutPublicExpansion(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	address := "http://" + listener.Addr().String()
	require.NoError(t, listener.Close())
	client, err := New(address, TransportOptions{RequestTimeout: time.Second})
	require.NoError(t, err)
	secret := "actual-admin-bearer-123"
	_, err = client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/readyz", Header: http.Header{"Authorization": {"Bearer " + secret}}})
	require.Error(t, err)
	problem := ClassifyRequestError(err, RequestPhaseRead)
	require.Equal(t, "gateway_not_running", problem.Code)
	var human bytes.Buffer
	require.NoError(t, WriteFailure(&human, OutputHuman, problem))
	require.Contains(t, human.String(), "connection refused")
	require.Contains(t, human.String(), address)
	require.NotContains(t, human.String(), secret)
	encoded, err := json.Marshal(problem)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "connection refused")
	require.NotContains(t, string(encoded), address)
	require.NotContains(t, string(encoded), "Local")
}

func TestOversizedLocalProblemHasUsableFallback(t *testing.T) {
	var stderr bytes.Buffer
	problem := &Problem{Code: "service_unavailable", Title: "permission denied: " + string(bytes.Repeat([]byte("x"), 10000)), Exit: 7}
	require.NoError(t, WriteFailure(&stderr, OutputHuman, problem))
	require.Contains(t, stderr.String(), "permission denied")
	require.Contains(t, stderr.String(), "truncated")
	require.LessOrEqual(t, stderr.Len(), maxProblemTitleBytes+1)
}
