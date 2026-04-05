package broker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

type mockServerManager struct{ mock.Mock }

func (m *mockServerManager) Tools() []server.Tool {
	args := m.Called()
	return args.Get(0).([]server.Tool)
}

func (m *mockServerManager) Call(ctx context.Context, tool string, arguments map[string]any) (*server.ToolResult, error) {
	args := m.Called(ctx, tool, arguments)
	return args.Get(0).(*server.ToolResult), args.Error(1)
}

type mockAuditLogger struct{ mock.Mock }

func (m *mockAuditLogger) Record(ctx context.Context, rec audit.Record) error {
	args := m.Called(ctx, rec)
	return args.Error(0)
}

func (m *mockAuditLogger) Query(ctx context.Context, opts audit.QueryOpts) ([]audit.Record, int, error) {
	args := m.Called(ctx, opts)
	return args.Get(0).([]audit.Record), args.Int(1), args.Error(2)
}

type mockApprover struct{ mock.Mock }

func (m *mockApprover) Review(ctx context.Context, tool string, args map[string]any) (bool, string, error) {
	a := m.Called(ctx, tool, args)
	return a.Bool(0), a.String(1), a.Error(2)
}

func TestBroker_Handle_AllowedTool(t *testing.T) {
	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "github.search", map[string]any{"q": "test"}).
		Return(&server.ToolResult{Content: "results"}, nil)

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "github.search" && r.Verdict == "allow"
	})).Return(nil)

	engine := rules.New([]config.RuleConfig{{Tool: "github.*", Verdict: "allow"}})

	b := &Broker{
		servers:  sm,
		rules:    engine,
		auditor:  al,
		approver: nil,
	}

	result, err := b.Handle(context.Background(), "github.search", map[string]any{"q": "test"})
	require.NoError(t, err)
	require.Equal(t, "results", result)

	sm.AssertExpectations(t)
	al.AssertExpectations(t)
}

func TestBroker_Handle_DeniedTool(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Verdict == "deny" && r.Error != ""
	})).Return(nil)

	engine := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "deny"}})

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: nil,
	}

	_, err := b.Handle(context.Background(), "anything", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}

func TestBroker_Handle_ApprovalRequired_Approved(t *testing.T) {
	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "fs.write", map[string]any{"path": "/tmp"}).
		Return(&server.ToolResult{Content: "ok"}, nil)

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.Anything).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, "fs.write", map[string]any{"path": "/tmp"}).Return(true, "", nil)

	engine := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})

	b := &Broker{
		servers:  sm,
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	result, err := b.Handle(context.Background(), "fs.write", map[string]any{"path": "/tmp"})
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

func TestBroker_Handle_ApprovalRequired_Denied(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.Anything).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, "fs.write", mock.Anything).Return(false, "", nil)

	engine := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	_, err := b.Handle(context.Background(), "fs.write", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}

func TestBroker_Handle_ApprovalRequired_DenialReasonPropagated(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.DenialReason == "timeout"
	})).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, "fs.write", mock.Anything).Return(false, "timeout", nil)

	engine := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	_, err := b.Handle(context.Background(), "fs.write", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
	al.AssertExpectations(t)
}
