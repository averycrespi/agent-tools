package broker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/grants"
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

type fakeGrantValidator struct {
	grant grants.Grant
	err   error
}

func (f fakeGrantValidator) ValidateToken(context.Context, string, time.Time) (grants.Grant, error) {
	return f.grant, f.err
}

type blockingApprover struct {
	started chan struct{}
	release chan struct{}
}

func (m *mockApprover) Review(ctx context.Context, request ApprovalRequest) (bool, string, error) {
	a := m.Called(ctx, request)
	return a.Bool(0), a.String(1), a.Error(2)
}

func (a *blockingApprover) Review(context.Context, ApprovalRequest) (bool, string, error) {
	close(a.started)
	<-a.release
	return true, "", nil
}

func approvalRequestFor(tool string, args map[string]any) any {
	return mock.MatchedBy(func(request ApprovalRequest) bool {
		return request.ToolName == tool && reflect.DeepEqual(args, request.ToolInput)
	})
}

type recordingObserver struct {
	requests []ApprovalRequest
	order    *[]string
}

func (o *recordingObserver) Observe(request ApprovalRequest) {
	o.requests = append(o.requests, request)
	if o.order != nil {
		*o.order = append(*o.order, "observe")
	}
}

type recordingApprover struct {
	request ApprovalRequest
	order   *[]string
}

func (a *recordingApprover) Review(_ context.Context, request ApprovalRequest) (bool, string, error) {
	a.request = request
	*a.order = append(*a.order, "review")
	return true, "", nil
}

func TestNewApprovalIDGenerates128RandomBitsAsHex(t *testing.T) {
	id, err := newApprovalID()
	require.NoError(t, err)
	require.Len(t, id, 32)
	decoded, err := hex.DecodeString(id)
	require.NoError(t, err)
	require.Len(t, decoded, 16)
}

func TestBroker_Handle_AllowedTool(t *testing.T) {
	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "github.search", map[string]any{"q": "test"}).
		Return(&server.ToolResult{Content: "results"}, nil)

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "github.search" && r.Verdict == "allow"
	})).Return(nil)

	engine, err := rules.New([]config.RuleConfig{{Tool: "github.*", Verdict: "allow"}})
	require.NoError(t, err)

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
		return r.Verdict == "deny" && r.DenialReason == "rule" && r.Error == "denied by rule"
	})).Return(nil)

	engine, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "deny"}})
	require.NoError(t, err)

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: nil,
	}

	_, err = b.Handle(context.Background(), "anything", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrDenied)
	require.Equal(t, "denied by policy: denied by rule", err.Error())
}

func TestBroker_Handle_DeniedToolWithRuleReason(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Verdict == "deny" && r.DenialReason == "rule: force pushes are disabled" && r.Error == "denied by rule: force pushes are disabled"
	})).Return(nil)

	engine, err := rules.New([]config.RuleConfig{{Tool: "git.push", Verdict: "deny", Reason: "force pushes are disabled"}})
	require.NoError(t, err)

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: nil,
	}

	_, err = b.Handle(context.Background(), "git.push", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrDenied)
	require.Equal(t, "denied by policy: denied by rule: force pushes are disabled", err.Error())
	al.AssertExpectations(t)
}

func TestBroker_Handle_ApprovalRequired_Approved(t *testing.T) {
	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "fs.write", map[string]any{"path": "/tmp"}).
		Return(&server.ToolResult{Content: "ok"}, nil)

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.Anything).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, approvalRequestFor("fs.write", map[string]any{"path": "/tmp"})).Return(true, "", nil)

	engine, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})
	require.NoError(t, err)

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

func TestBroker_Handle_RequireApprovalObservesBrokerOwnedRequestBeforeReview(t *testing.T) {
	fixedTime := time.Date(2026, 8, 3, 18, 42, 0, 0, time.FixedZone("offset", 3600))
	input := map[string]any{"path": "/tmp/file"}
	order := []string{}
	observer := &recordingObserver{order: &order}
	approver := &recordingApprover{order: &order}

	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "fs.write", input).Return(&server.ToolResult{Content: "ok"}, nil)
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.Anything).Return(nil)
	engine, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})
	require.NoError(t, err)

	b := NewWithOptions(Options{
		Servers: sm, Rules: engine, Auditor: al, Approver: approver, Observer: observer,
		Clock:              func() time.Time { return fixedTime },
		GenerateApprovalID: func() (string, error) { return "c9cb427bc1387a23c9cb427bc1387a23", nil },
	})
	_, err = b.Handle(context.Background(), "fs.write", input)
	require.NoError(t, err)

	require.Equal(t, []string{"observe", "review"}, order)
	require.Len(t, observer.requests, 1)
	request := observer.requests[0]
	require.Equal(t, approver.request, request)
	require.Equal(t, "c9cb427bc1387a23c9cb427bc1387a23", request.ID)
	require.Equal(t, fixedTime.UTC(), request.OccurredAt)
	require.Equal(t, ApprovalPolicy{Verdict: "require-approval", RuleSource: "base"}, request.Policy)
	require.Nil(t, request.Grant)
}

func TestBroker_Handle_RequireApprovalObserverSuppressedBeforeFanoutTransition(t *testing.T) {
	tests := []struct {
		name         string
		verdict      string
		options      HandleOptions
		withApprover bool
		cancel       bool
	}{
		{name: "allow", verdict: "allow", withApprover: true},
		{name: "deny", verdict: "deny", withApprover: true},
		{name: "reject mode", verdict: "require-approval", options: HandleOptions{ApprovalMode: ApprovalModeReject}, withApprover: true},
		{name: "no approver", verdict: "require-approval"},
		{name: "already canceled", verdict: "require-approval", withApprover: true, cancel: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			engine, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: tc.verdict}})
			require.NoError(t, err)
			observer := &recordingObserver{}
			al := new(mockAuditLogger)
			al.On("Record", mock.Anything, mock.Anything).Return(nil)
			sm := new(mockServerManager)
			if tc.verdict == "allow" || tc.cancel {
				sm.On("Call", mock.Anything, "tool", mock.Anything).Return(&server.ToolResult{Content: "ok"}, nil)
			}
			var approver Approver
			order := []string{}
			if tc.withApprover {
				approver = &recordingApprover{order: &order}
			}
			b := NewWithOptions(Options{
				Servers: sm, Rules: engine, Auditor: al, Approver: approver, Observer: observer,
				GenerateApprovalID: func() (string, error) { return "approval-id", nil },
			})
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, _ = b.HandleToolResultWithOptions(ctx, "tool", nil, tc.options)
			require.Empty(t, observer.requests)
		})
	}
}

func TestBroker_Handle_RequireApprovalIncludesValidGrantAttribution(t *testing.T) {
	for _, tc := range []struct {
		name       string
		grantRules []config.RuleConfig
		baseRules  []config.RuleConfig
		source     string
	}{
		{name: "grant rule", grantRules: []config.RuleConfig{{Tool: "fs.write", Verdict: "require-approval"}}, baseRules: []config.RuleConfig{{Tool: "*", Verdict: "deny"}}, source: "grant"},
		{name: "grant fallthrough", grantRules: []config.RuleConfig{{Tool: "github.*", Verdict: "allow"}}, baseRules: []config.RuleConfig{{Tool: "*", Verdict: "require-approval"}}, source: "base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := grants.Grant{ID: "grant-1", Name: "release", Fingerprint: "abc123", Status: "active", Rules: tc.grantRules}
			engine, err := rules.New(tc.baseRules)
			require.NoError(t, err)
			observer := &recordingObserver{}
			order := []string{}
			approver := &recordingApprover{order: &order}
			sm := new(mockServerManager)
			sm.On("Call", mock.Anything, "fs.write", mock.Anything).Return(&server.ToolResult{Content: "ok"}, nil)
			al := new(mockAuditLogger)
			al.On("Record", mock.Anything, mock.Anything).Return(nil)
			b := NewWithOptions(Options{
				Servers: sm, Rules: engine, Auditor: al, Approver: approver,
				Grants: fakeGrantValidator{grant: grant}, Observer: observer,
				GenerateApprovalID: func() (string, error) { return "approval-id", nil },
			})

			_, err = b.HandleToolResultWithOptions(context.Background(), "fs.write", nil, HandleOptions{GrantToken: "secret"})
			require.NoError(t, err)
			require.Len(t, observer.requests, 1)
			require.Equal(t, tc.source, observer.requests[0].Policy.RuleSource)
			require.Equal(t, &ApprovalGrant{ID: "grant-1", Name: "release", Fingerprint: "abc123", Status: "active"}, observer.requests[0].Grant)
		})
	}
}

func TestBroker_Handle_ApprovalIDGenerationFailureFailsClosed(t *testing.T) {
	engine, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})
	require.NoError(t, err)
	observer := &recordingObserver{}
	order := []string{}
	approver := &recordingApprover{order: &order}
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(rec audit.Record) bool {
		return rec.Error == "approval request ID generation failed" && rec.Approved != nil && !*rec.Approved
	})).Return(nil)
	b := NewWithOptions(Options{
		Servers: new(mockServerManager), Rules: engine, Auditor: al, Approver: approver, Observer: observer,
		GenerateApprovalID: func() (string, error) { return "", errors.New("entropy unavailable") },
	})

	_, err = b.Handle(context.Background(), "fs.write", nil)
	require.ErrorIs(t, err, ErrDenied)
	require.Empty(t, observer.requests)
	require.Empty(t, order)
}

func TestBroker_Handle_ApprovalInProgressKeepsPreReloadDecision(t *testing.T) {
	store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})
	require.NoError(t, err)

	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "fs.write", map[string]any{"path": "/tmp/file"}).
		Return(&server.ToolResult{Content: "ok"}, nil)

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "fs.write" && r.Verdict == "require-approval" && r.Approved != nil && *r.Approved
	})).Return(nil)

	ap := &blockingApprover{started: make(chan struct{}), release: make(chan struct{})}
	b := New(sm, store, al, ap, nil)

	done := make(chan error, 1)
	go func() {
		_, err := b.Handle(context.Background(), "fs.write", map[string]any{"path": "/tmp/file"})
		done <- err
	}()

	<-ap.started
	require.NoError(t, store.Reload([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "reloaded"}}))
	close(ap.release)

	require.NoError(t, <-done)
	require.Equal(t, rules.Deny, store.EvaluateWithMetadata("fs.write", nil).Verdict)
	sm.AssertExpectations(t)
	al.AssertExpectations(t)
}

func TestBroker_Handle_ApprovalRequired_Denied(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.Anything).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, approvalRequestFor("fs.write", nil)).Return(false, "", nil)

	engine, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})
	require.NoError(t, err)

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	_, err = b.Handle(context.Background(), "fs.write", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}

func TestBroker_Handle_ApprovalRequired_DenialReasonPropagated(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.DenialReason == "timeout" && r.Error == "denied by timeout"
	})).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, approvalRequestFor("fs.write", nil)).Return(false, "timeout", nil)

	engine, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})
	require.NoError(t, err)

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	_, err = b.Handle(context.Background(), "fs.write", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrDenied)
	require.Equal(t, "denied by policy: denied by timeout", err.Error())
	al.AssertExpectations(t)
}

func TestBroker_Handle_ApprovalRequired_RejectModeDoesNotCallApprover(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "fs.write" &&
			r.Verdict == "require-approval" &&
			r.Approved != nil &&
			!*r.Approved &&
			r.DenialReason == "approval-mode: reject" &&
			r.Error == "tool call blocked: approval is required for fs.write, but this request uses Mcp-Broker-Approval-Mode=reject"
	})).Return(nil)

	ap := new(mockApprover)
	sm := new(mockServerManager)

	engine, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})
	require.NoError(t, err)

	b := &Broker{
		servers:  sm,
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	_, err = b.HandleToolResultWithOptions(context.Background(), "fs.write", nil, HandleOptions{ApprovalMode: ApprovalModeReject})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrDenied)
	require.Equal(t, "denied by policy: tool call blocked: approval is required for fs.write, but this request uses Mcp-Broker-Approval-Mode=reject", err.Error())
	ap.AssertNotCalled(t, "Review", mock.Anything, mock.Anything, mock.Anything)
	sm.AssertNotCalled(t, "Call", mock.Anything, mock.Anything, mock.Anything)
	al.AssertExpectations(t)
}

func TestBroker_Handle_GrantAllowShadowsBaseDeny(t *testing.T) {
	grant := grants.Grant{
		ID:          "grant-1",
		Name:        "release",
		Fingerprint: "abc123def456",
		Status:      "active",
		Rules:       []config.RuleConfig{{Tool: "git.push", Verdict: "allow", Reason: "release window"}},
	}

	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "git.push", map[string]any{"branch": "main"}).Return(&server.ToolResult{Content: "pushed"}, nil)

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "git.push" &&
			r.Verdict == "allow" &&
			r.RuleSource == "grant" &&
			r.GrantID == "grant-1" &&
			r.GrantName == "release" &&
			r.GrantFingerprint == "abc123def456" &&
			r.GrantStatus == "active"
	})).Return(nil)

	base, err := rules.New([]config.RuleConfig{{Tool: "git.*", Verdict: "deny", Reason: "base deny"}})
	require.NoError(t, err)

	b := NewWithGrants(sm, base, al, nil, fakeGrantValidator{grant: grant}, nil)
	result, err := b.HandleToolResultWithOptions(context.Background(), "git.push", map[string]any{"branch": "main"}, HandleOptions{GrantToken: "secret"})
	require.NoError(t, err)
	require.Equal(t, "pushed", result.Content)
	sm.AssertExpectations(t)
	al.AssertExpectations(t)
}

func TestBroker_Handle_GrantDenyShadowsBaseAllow(t *testing.T) {
	grant := grants.Grant{
		ID:          "grant-1",
		Name:        "blocker",
		Fingerprint: "abc123def456",
		Status:      "active",
		Rules:       []config.RuleConfig{{Tool: "git.push", Verdict: "deny", Reason: "temporary block"}},
	}

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "git.push" &&
			r.Verdict == "deny" &&
			r.RuleSource == "grant" &&
			r.DenialReason == "grant rule: temporary block" &&
			r.Error == "denied by grant rule: temporary block"
	})).Return(nil)

	base, err := rules.New([]config.RuleConfig{{Tool: "git.*", Verdict: "allow"}})
	require.NoError(t, err)
	sm := new(mockServerManager)
	b := NewWithGrants(sm, base, al, nil, fakeGrantValidator{grant: grant}, nil)

	_, err = b.HandleToolResultWithOptions(context.Background(), "git.push", nil, HandleOptions{GrantToken: "secret"})
	require.ErrorIs(t, err, ErrDenied)
	sm.AssertNotCalled(t, "Call", mock.Anything, mock.Anything, mock.Anything)
	al.AssertExpectations(t)
}

func TestBroker_Handle_GrantFallthroughUsesBaseRules(t *testing.T) {
	grant := grants.Grant{
		ID:          "grant-1",
		Name:        "narrow",
		Fingerprint: "abc123def456",
		Status:      "active",
		Rules:       []config.RuleConfig{{Tool: "github.*", Verdict: "allow"}},
	}

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "git.push" &&
			r.Verdict == "deny" &&
			r.RuleSource == "base" &&
			r.GrantID == "grant-1" &&
			r.DenialReason == "rule: base deny"
	})).Return(nil)

	base, err := rules.New([]config.RuleConfig{{Tool: "git.*", Verdict: "deny", Reason: "base deny"}})
	require.NoError(t, err)
	b := NewWithGrants(new(mockServerManager), base, al, nil, fakeGrantValidator{grant: grant}, nil)

	_, err = b.HandleToolResultWithOptions(context.Background(), "git.push", nil, HandleOptions{GrantToken: "secret"})
	require.ErrorIs(t, err, ErrDenied)
	al.AssertExpectations(t)
}

func TestBroker_Handle_GrantRequireApprovalRejectMode(t *testing.T) {
	grant := grants.Grant{
		ID:          "grant-1",
		Name:        "review",
		Fingerprint: "abc123def456",
		Status:      "active",
		Rules:       []config.RuleConfig{{Tool: "fs.write", Verdict: "require-approval"}},
	}

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Verdict == "require-approval" &&
			r.RuleSource == "grant" &&
			r.Approved != nil &&
			!*r.Approved &&
			r.DenialReason == "approval-mode: reject"
	})).Return(nil)

	base, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "allow"}})
	require.NoError(t, err)
	ap := new(mockApprover)
	sm := new(mockServerManager)
	b := NewWithGrants(sm, base, al, ap, fakeGrantValidator{grant: grant}, nil)

	_, err = b.HandleToolResultWithOptions(context.Background(), "fs.write", nil, HandleOptions{GrantToken: "secret", ApprovalMode: ApprovalModeReject})
	require.ErrorIs(t, err, ErrDenied)
	ap.AssertNotCalled(t, "Review", mock.Anything, mock.Anything, mock.Anything)
	sm.AssertNotCalled(t, "Call", mock.Anything, mock.Anything, mock.Anything)
	al.AssertExpectations(t)
}

func TestBroker_Handle_InvalidGrantFailsClosedBeforeApprovalOrBackend(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "git.push" &&
			r.Verdict == "deny" &&
			r.RuleSource == "none/default" &&
			r.GrantStatus == "invalid" &&
			r.DenialReason == "grant: invalid" &&
			r.Error == "invalid grant token"
	})).Return(nil)

	base, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "allow"}})
	require.NoError(t, err)
	ap := new(mockApprover)
	sm := new(mockServerManager)
	observer := &recordingObserver{}
	b := NewWithOptions(Options{
		Servers: sm, Rules: base, Auditor: al, Approver: ap,
		Grants: fakeGrantValidator{err: grants.ErrUnknown}, Observer: observer,
	})

	_, err = b.HandleToolResultWithOptions(context.Background(), "git.push", nil, HandleOptions{GrantToken: "bad"})
	require.ErrorIs(t, err, ErrDenied)
	ap.AssertNotCalled(t, "Review", mock.Anything, mock.Anything)
	require.Empty(t, observer.requests)
	sm.AssertNotCalled(t, "Call", mock.Anything, mock.Anything, mock.Anything)
	al.AssertExpectations(t)
}

func TestBroker_Handle_ExpiredAndRevokedGrantsFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		status     string
		wantError  string
		wantReason string
	}{
		{name: "expired", err: grants.ErrExpired, status: "expired", wantError: "grant expired", wantReason: "grant: expired"},
		{name: "revoked", err: grants.ErrRevoked, status: "revoked", wantError: "grant revoked", wantReason: "grant: revoked"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grant := grants.Grant{ID: "grant-1", Name: tt.name, Fingerprint: "abc123def456", Status: tt.status}
			al := new(mockAuditLogger)
			al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
				return r.Verdict == "deny" &&
					r.GrantID == "grant-1" &&
					r.GrantStatus == tt.status &&
					r.DenialReason == tt.wantReason &&
					r.Error == tt.wantError
			})).Return(nil)

			base, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "allow"}})
			require.NoError(t, err)
			observer := &recordingObserver{}
			b := NewWithOptions(Options{
				Servers: new(mockServerManager), Rules: base, Auditor: al,
				Grants: fakeGrantValidator{grant: grant, err: tt.err}, Observer: observer,
			})

			_, err = b.HandleToolResultWithOptions(context.Background(), "git.push", nil, HandleOptions{GrantToken: "secret"})
			require.ErrorIs(t, err, ErrDenied)
			require.Empty(t, observer.requests)
			al.AssertExpectations(t)
		})
	}
}

func TestBroker_Handle_GrantStoreErrorFailsClosed(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Verdict == "deny" && r.GrantStatus == "store-error" && r.Error == "grant validation failed"
	})).Return(nil)

	base, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "allow"}})
	require.NoError(t, err)
	b := NewWithGrants(new(mockServerManager), base, al, nil, fakeGrantValidator{err: errors.New("db locked")}, nil)

	_, err = b.HandleToolResultWithOptions(context.Background(), "git.push", nil, HandleOptions{GrantToken: "secret"})
	require.ErrorIs(t, err, ErrDenied)
	al.AssertExpectations(t)
}

func TestBroker_Handle_ApprovalRequired_UserDenialReasonFormatted(t *testing.T) {
	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.DenialReason == "user: needs narrower scope" && r.Error == "denied by user: needs narrower scope"
	})).Return(nil)

	ap := new(mockApprover)
	ap.On("Review", mock.Anything, approvalRequestFor("fs.write", nil)).Return(false, "user: needs narrower scope", nil)

	engine, err := rules.New([]config.RuleConfig{{Tool: "*", Verdict: "require-approval"}})
	require.NoError(t, err)

	b := &Broker{
		servers:  new(mockServerManager),
		rules:    engine,
		auditor:  al,
		approver: ap,
	}

	_, err = b.Handle(context.Background(), "fs.write", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrDenied)
	require.Equal(t, "denied by policy: denied by user: needs narrower scope", err.Error())
	al.AssertExpectations(t)
}

// Arg-constrained allow rule triggers: call args satisfy the constraint →
// backend Call invoked, verdict=allow recorded.
func TestBroker_Handle_ArgConstrainedAllowRule_Triggers(t *testing.T) {
	callArgs := map[string]any{"branch": "main", "force": false}

	sm := new(mockServerManager)
	sm.On("Call", mock.Anything, "git.push", callArgs).
		Return(&server.ToolResult{Content: "pushed"}, nil)

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "git.push" && r.Verdict == "allow"
	})).Return(nil)

	engine, err := rules.New([]config.RuleConfig{
		{
			Tool:    "git.push",
			Verdict: "allow",
			Args: []config.ArgPattern{
				{Path: "branch", Match: json.RawMessage(`"main"`)},
			},
		},
		{Tool: "*", Verdict: "deny"},
	})
	require.NoError(t, err)

	b := &Broker{
		servers:  sm,
		rules:    engine,
		auditor:  al,
		approver: nil,
	}

	result, err := b.Handle(context.Background(), "git.push", callArgs)
	require.NoError(t, err)
	require.Equal(t, "pushed", result)

	sm.AssertExpectations(t)
	al.AssertExpectations(t)
}

// Arg-constrained deny rule blocks: call args satisfy a deny constraint →
// ErrDenied returned, no backend Call.
func TestBroker_Handle_ArgConstrainedDenyRule_Blocks(t *testing.T) {
	callArgs := map[string]any{"branch": "main", "force": true}

	al := new(mockAuditLogger)
	al.On("Record", mock.Anything, mock.MatchedBy(func(r audit.Record) bool {
		return r.Tool == "git.push" && r.Verdict == "deny" && r.Error != ""
	})).Return(nil)

	engine, err := rules.New([]config.RuleConfig{
		{
			Tool:    "git.push",
			Verdict: "deny",
			Args: []config.ArgPattern{
				{Path: "force", Match: json.RawMessage(`"true"`)},
			},
		},
		{Tool: "git.*", Verdict: "allow"},
	})
	require.NoError(t, err)

	sm := new(mockServerManager)
	// Call must NOT be invoked

	b := &Broker{
		servers:  sm,
		rules:    engine,
		auditor:  al,
		approver: nil,
	}

	_, err = b.Handle(context.Background(), "git.push", callArgs)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrDenied)

	sm.AssertNotCalled(t, "Call", mock.Anything, mock.Anything, mock.Anything)
	al.AssertExpectations(t)
}
