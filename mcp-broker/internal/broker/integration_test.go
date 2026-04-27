//go:build integration

package broker

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

func TestBroker_Integration_FullPipeline(t *testing.T) {
	// Real audit logger
	dbPath := filepath.Join(t.TempDir(), "test-audit.db")
	auditor, err := audit.NewLogger(dbPath)
	require.NoError(t, err)
	defer func() { _ = auditor.Close(context.Background()) }()

	// Real rules engine
	engine, err := rules.New([]config.RuleConfig{
		{Tool: "echo.*", Verdict: "allow"},
		{Tool: "*", Verdict: "deny"},
	})
	require.NoError(t, err)

	// Mock server manager
	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "echo.ping", map[string]any{"message": "hello"}).
		Return(&server.ToolResult{Content: map[string]any{"response": "hello"}}, nil)
	sm.On("Tools").Return([]server.Tool{
		{Name: "echo.ping", Description: "Echo a message"},
	})

	b := New(sm, engine, auditor, nil, nil)

	// Allowed call
	result, err := b.Handle(context.Background(), "echo.ping", map[string]any{"message": "hello"})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Denied call
	_, err = b.Handle(context.Background(), "fs.delete", nil)
	require.Error(t, err)

	// Verify audit records
	records, total, err := auditor.Query(context.Background(), audit.QueryOpts{Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, records, 2)

	// Most recent first (denied)
	require.Equal(t, "deny", records[0].Verdict)
	require.Equal(t, "allow", records[1].Verdict)
}
