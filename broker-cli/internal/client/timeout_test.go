package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/stretchr/testify/require"
)

func TestNewWithTimeoutReturnsWhenBackendBlocks(t *testing.T) {
	unblock := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(unblock)

	start := time.Now()
	_, err := client.NewWithTimeout(context.Background(), server.URL+"/mcp", "token", 20*time.Millisecond)

	require.Error(t, err)
	require.Less(t, time.Since(start), time.Second)
	require.ErrorContains(t, err, "initialize MCP client")
}
