//go:build integration

package client_test

import (
	"context"
	"os"
	"testing"

	"github.com/averycrespi/agent-tools/broker-cli/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func brokerClient(t *testing.T) client.Client {
	t.Helper()
	endpoint := os.Getenv("MCP_BROKER_ENDPOINT")
	token := os.Getenv("MCP_BROKER_AUTH_TOKEN")
	if endpoint == "" || token == "" {
		t.Skip("MCP_BROKER_ENDPOINT and MCP_BROKER_AUTH_TOKEN required")
	}
	c, err := client.New(context.Background(), endpoint+"/mcp", token)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestListTools_returnsTools(t *testing.T) {
	c := brokerClient(t)
	tools, err := c.ListTools(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, tools)
}

func TestCallTool_unknownTool(t *testing.T) {
	c := brokerClient(t)
	_, err := c.CallTool(context.Background(), "no.such.tool", nil)
	assert.Error(t, err)
}
