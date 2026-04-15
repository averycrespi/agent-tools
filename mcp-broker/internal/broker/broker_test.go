package broker

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/grants"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

func openInMemSQLite(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

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

// captureAuditor records all audit.Record calls for assertion in grant tests.
type captureAuditor struct {
	records []audit.Record
}

func (c *captureAuditor) Record(_ context.Context, rec audit.Record) error {
	c.records = append(c.records, rec)
	return nil
}

func (c *captureAuditor) Query(_ context.Context, _ audit.QueryOpts) ([]audit.Record, int, error) {
	return c.records, len(c.records), nil
}

// fakeServerManager returns a canned string result.
type fakeServerManager struct {
	resp string
}

func (f *fakeServerManager) Tools() []server.Tool { return nil }

func (f *fakeServerManager) Call(_ context.Context, _ string, _ map[string]any) (*server.ToolResult, error) {
	return &server.ToolResult{Content: f.resp}, nil
}

func TestHandleGrantMatchedSkipsRulesAndApproval(t *testing.T) {
	// Rules say deny, but a matching grant must authorize the call.
	ctx := context.Background()
	db := openInMemSQLite(t)
	store, err := grants.NewStore(ctx, db)
	require.NoError(t, err)
	eng := grants.NewEngine(store)

	cred, err := grants.NewCredential()
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, store.Create(ctx, grants.Grant{
		ID:        cred.ID,
		Entries:   []grants.Entry{{Tool: "x.y", ArgSchema: json.RawMessage(`{"type":"object"}`)}},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}, cred.TokenHash))

	fa := &captureAuditor{}
	denyRules := rules.New([]config.RuleConfig{{Tool: "x.y", Verdict: "deny"}})

	b := New(&fakeServerManager{resp: "ok"}, denyRules, fa, nil, nil, eng)

	ctx = grants.ContextWithToken(ctx, cred.Token)
	got, err := b.Handle(ctx, "x.y", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "ok", got)

	require.Len(t, fa.records, 1)
	require.Equal(t, "matched", fa.records[0].GrantOutcome)
	require.Equal(t, cred.ID, fa.records[0].GrantID)
	require.Equal(t, "allow", fa.records[0].Verdict)
}

func TestHandleGrantFellThroughAppliesRules(t *testing.T) {
	// A valid token is presented but the tool doesn't match the grant entry.
	// Rules should evaluate as usual; grant_outcome=fell_through.
	ctx := context.Background()
	db := openInMemSQLite(t)
	store, err := grants.NewStore(ctx, db)
	require.NoError(t, err)
	eng := grants.NewEngine(store)

	cred, err := grants.NewCredential()
	require.NoError(t, err)
	now := time.Now().UTC()
	// Grant only covers "other.tool", not "x.y"
	require.NoError(t, store.Create(ctx, grants.Grant{
		ID:        cred.ID,
		Entries:   []grants.Entry{{Tool: "other.tool", ArgSchema: json.RawMessage(`{"type":"object"}`)}},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}, cred.TokenHash))

	fa := &captureAuditor{}
	allowRules := rules.New([]config.RuleConfig{{Tool: "x.y", Verdict: "allow"}})

	sm := &fakeServerManager{resp: "ok"}
	b := New(sm, allowRules, fa, nil, nil, eng)

	ctx = grants.ContextWithToken(ctx, cred.Token)
	got, err := b.Handle(ctx, "x.y", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "ok", got)

	require.Len(t, fa.records, 1)
	require.Equal(t, "fell_through", fa.records[0].GrantOutcome)
	require.Equal(t, cred.ID, fa.records[0].GrantID)
	require.Equal(t, "allow", fa.records[0].Verdict)
}

func TestHandleInvalidTokenDoesNotDeny(t *testing.T) {
	// Bogus token: grant_outcome=invalid, rules decide (allow here).
	ctx := context.Background()
	db := openInMemSQLite(t)
	store, err := grants.NewStore(ctx, db)
	require.NoError(t, err)
	eng := grants.NewEngine(store)

	fa := &captureAuditor{}
	allowRules := rules.New([]config.RuleConfig{{Tool: "x.y", Verdict: "allow"}})

	sm := &fakeServerManager{resp: "ok"}
	b := New(sm, allowRules, fa, nil, nil, eng)

	ctx = grants.ContextWithToken(ctx, "bogus-token-that-does-not-exist")
	got, err := b.Handle(ctx, "x.y", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "ok", got)

	require.Len(t, fa.records, 1)
	require.Equal(t, "invalid", fa.records[0].GrantOutcome)
	require.Empty(t, fa.records[0].GrantID)
}
