package selfservice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelfServiceResolverPinsExactlySixStrictHandlers(t *testing.T) {
	service, _, _, _ := newHandlerService(t)
	for _, tool := range contract.SyntheticSelfServiceTools() {
		target, found := service.Resolve(tool.ExternalName)
		require.True(t, found, tool.ExternalName)
		assert.NoError(t, target.Validate(validHandlerArguments(tool.UpstreamName)))
	}
	_, found := service.Resolve("mcp_gateway.not_a_tool")
	assert.False(t, found)
	_, found = service.Resolve("sample.echo")
	assert.False(t, found)

	invalid := map[string][]string{
		"mcp_gateway.get_identity": {`{"extra":true}`},
		"mcp_gateway.list_grants":  {`{"cursor":""}`, `{"cursor":"not-a-cursor"}`, `{"cursor":null,"extra":true}`},
		"mcp_gateway.create_grant_request": {
			`{"policy":{"scope":"tool","target":"sample.echo","constraint":null,"duration_seconds":"59","future_tools_acknowledged":false}}`,
			`{"policy":{"scope":"tool","target":"sample.echo","constraint":null,"duration_seconds":"060","future_tools_acknowledged":false}}`,
			`{"policy":{"scope":"tool","target":"sample.echo","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true}}`,
			`{"policy":{"scope":"server","target":"sample","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false}}`,
			`{"policy":{"scope":"server","target":"sample","constraint":{"equals":{"/x":1}},"duration_seconds":null,"future_tools_acknowledged":true}}`,
		},
		"mcp_gateway.get_grant_request":    {`{"id":"bad"}`},
		"mcp_gateway.list_grant_requests":  {`{"cursor":null,"state":"foreign"}`, `{"cursor":"bad","state":null}`},
		"mcp_gateway.cancel_grant_request": {`{"id":null}`},
	}
	for name, values := range invalid {
		target, resolved := service.Resolve(name)
		require.True(t, resolved)
		for _, raw := range values {
			assert.Error(t, target.Validate(handlerArguments(raw)), name+" "+raw)
		}
	}
}

func TestSelfServiceHandlersReturnFixedSummariesAndExactStructuredResults(t *testing.T) {
	service, projections, requests, subject := newHandlerService(t)
	identity := contract.SelfIdentity{ID: subject.PrincipalID(), DisplayName: "Agent", State: contract.PrincipalActive, Visibility: contract.VisibilityRequestable, PrincipalRevision: subject.PrincipalRevision(), CredentialRevision: subject.CredentialRevision()}
	projections.identity = identity
	description := "Sample access"
	grant := contract.AgentGrant{ID: selfserviceID(610), Description: &description, Effect: contract.GrantAllow, Policy: contract.GrantPolicy{Scope: contract.PolicyServer, Target: "sample"}, State: contract.GrantActive, CreatedAt: "2026-08-27T00:00:00.000000000Z"}
	projections.grants = authorization.SelfGrantPage{Items: []contract.AgentGrant{grant}}
	request := contract.AgentGrantRequest{ID: selfserviceID(611), State: contract.RequestPending, Revision: "1", RequestedPolicy: contract.Policy{Scope: contract.PolicyTool, Target: "sample.echo"}, CreatedAt: "2026-08-27T00:00:00.000000000Z", UpdatedAt: "2026-08-27T00:00:00.000000000Z"}
	requests.create = contract.CreateGrantRequestResult{Outcome: contract.RequestCreated, Request: &request}
	requests.get = request
	requests.found = true
	requests.list = grantrequests.SelfPage{Items: []contract.AgentGrantRequest{request}}
	requests.cancel = contract.CancelGrantRequestResult{Outcome: contract.RequestCancellationCancelled, Request: &request}

	tests := []struct {
		name    string
		summary string
		result  invocation.LocalCallResult
		want    any
	}{
		{name: "identity", summary: contract.SummaryIdentityReturned, result: service.handleGetIdentity(context.Background(), subject, handlerArguments(`{}`)), want: contract.GetIdentityResult{Identity: identity}},
		{name: "grants", summary: contract.SummaryGrantsReturned, result: service.handleListGrants(context.Background(), subject, handlerArguments(`{"cursor":null}`)), want: contract.ListGrantsResult{Outcome: contract.CursorOK, Items: []contract.AgentGrant{grant}}},
		{name: "create", summary: contract.SummaryGrantRequestProcessed, result: service.handleCreateGrantRequest(context.Background(), subject, handlerArguments(`{"policy":{"scope":"tool","target":"sample.echo","constraint":{"equals":{"/value":1e0}},"duration_seconds":null,"future_tools_acknowledged":false}}`)), want: requests.create},
		{name: "get", summary: contract.SummaryGrantRequestReturned, result: service.handleGetGrantRequest(context.Background(), subject, handlerArguments(`{"id":"`+request.ID+`"}`)), want: contract.GetGrantRequestResult{Outcome: contract.RequestFound, Request: &request}},
		{name: "list requests", summary: contract.SummaryGrantRequestsReturned, result: service.handleListGrantRequests(context.Background(), subject, handlerArguments(`{"cursor":null,"state":null}`)), want: contract.ListGrantRequestsResult{Outcome: contract.CursorOK, Items: []contract.AgentGrantRequest{request}}},
		{name: "cancel", summary: contract.SummaryGrantRequestCancellationProcessed, result: service.handleCancelGrantRequest(context.Background(), subject, handlerArguments(`{"id":"`+request.ID+`"}`)), want: requests.cancel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := invocation.SanitizeLocalCallResult(test.result)
			require.NotNil(t, outcome.Result)
			require.Len(t, outcome.Result.Content, 1)
			assert.JSONEq(t, `{"type":"text","text":"`+test.summary+`"}`, string(outcome.Result.Content[0]))
			want, err := json.Marshal(test.want)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(outcome.Result.StructuredContent))
		})
	}
	assert.Equal(t, subject.PrincipalID(), requests.principalID)
	require.NotNil(t, requests.createdPolicy.Constraint)
	assert.Contains(t, string(*requests.createdPolicy.Constraint), "1e0", "lexical constraint numbers must survive decoding")
}

func TestSelfServiceCursorOutcomesAndStorageCertaintyAreClosed(t *testing.T) {
	service, projections, requests, subject := newHandlerService(t)
	badMAC := validGrantCursor(t, service.cursors, subject)
	frame, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(badMAC, cursorPrefix))
	require.NoError(t, err)
	frame[len(frame)-1] ^= 1
	badMAC = cursorPrefix + base64.RawURLEncoding.EncodeToString(frame)
	result := service.handleListGrants(context.Background(), subject, handlerArguments(`{"cursor":"`+badMAC+`"}`))
	assertStructuredOutcome(t, result, contract.CursorInvalid)

	projections.err = authorization.ErrStaleCursor
	result = service.handleListGrants(context.Background(), subject, handlerArguments(`{"cursor":null}`))
	assertStructuredOutcome(t, result, contract.CursorStale)
	projections.err = errors.New("read failed")
	known := invocation.SanitizeLocalCallResult(service.handleGetIdentity(context.Background(), subject, handlerArguments(`{}`)))
	assert.Equal(t, contract.ToolUnavailable, known.ErrorCode)
	assert.Equal(t, contract.TerminalPrestartFailure, known.TerminalClass)

	requests.createErr = grantrequests.ErrStorageOutcomeUncertain
	uncertain := invocation.SanitizeLocalCallResult(service.handleCreateGrantRequest(context.Background(), subject, handlerArguments(`{"policy":{"scope":"tool","target":"sample.echo","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false}}`)))
	assert.Equal(t, contract.ToolUnavailable, uncertain.ErrorCode)
	assert.Empty(t, uncertain.TerminalClass)
	assert.NotEqual(t, contract.OutcomeUnknown, uncertain.ErrorCode)
}

func TestDrainSelfServiceReportsPostCommitUncertaintyWithoutTerminalReplay(t *testing.T) {
	service, _, requests, subject := newHandlerService(t)
	requests.createErr = grantrequests.ErrStorageOutcomeUncertain
	result := invocation.SanitizeLocalCallResult(service.handleCreateGrantRequest(context.Background(), subject, handlerArguments(`{"policy":{"scope":"tool","target":"sample.echo","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false}}`)))
	assert.Equal(t, contract.ToolUnavailable, result.ErrorCode)
	assert.Empty(t, result.TerminalClass)
	assert.NotEqual(t, contract.OutcomeUnknown, result.ErrorCode)
}

type fakeHandlerProjections struct {
	identity contract.SelfIdentity
	grants   authorization.SelfGrantPage
	err      error
}

func (reader *fakeHandlerProjections) ReadSelfIdentity(context.Context, authorization.AdmittedSubject) (contract.SelfIdentity, error) {
	return reader.identity, reader.err
}
func (reader *fakeHandlerProjections) ListSelfGrants(context.Context, authorization.AdmittedSubject, *authorization.SelfGrantCursor, int) (authorization.SelfGrantPage, error) {
	return reader.grants, reader.err
}

type fakeHandlerRequests struct {
	create        contract.CreateGrantRequestResult
	createErr     error
	cancel        contract.CancelGrantRequestResult
	cancelErr     error
	get           contract.AgentGrantRequest
	found         bool
	list          grantrequests.SelfPage
	principalID   string
	createdPolicy contract.Policy
}

func (repository *fakeHandlerRequests) CreateOrExisting(_ context.Context, request grantrequests.CreateRequest) (contract.CreateGrantRequestResult, error) {
	repository.principalID, repository.createdPolicy = request.PrincipalID, request.Policy
	return repository.create, repository.createErr
}
func (repository *fakeHandlerRequests) CancelOwned(_ context.Context, principalID, _ string) (contract.CancelGrantRequestResult, error) {
	repository.principalID = principalID
	return repository.cancel, repository.cancelErr
}
func (repository *fakeHandlerRequests) GetOwned(_ context.Context, principalID, _ string) (contract.AgentGrantRequest, bool, error) {
	repository.principalID = principalID
	return repository.get, repository.found, nil
}
func (repository *fakeHandlerRequests) ListOwned(_ context.Context, principalID string, _ *contract.GrantRequestState, _ *grantrequests.SelfCursor, _ int) (grantrequests.SelfPage, error) {
	repository.principalID = principalID
	return repository.list, nil
}

func newHandlerService(t *testing.T) (*Service, *fakeHandlerProjections, *fakeHandlerRequests, authorization.AdmittedSubject) {
	t.Helper()
	authority, _, subject, _ := newAdmittedSubject(t)
	projections := &fakeHandlerProjections{}
	requests := &fakeHandlerRequests{}
	codec, err := NewCursorCodec(bytes.NewReader(bytes.Repeat([]byte{0x55}, cursorKeyBytes)), selfserviceID(901))
	require.NoError(t, err)
	service, err := NewService(authority, projections, requests, codec)
	require.NoError(t, err)
	return service, projections, requests, subject
}

func validHandlerArguments(name string) strictjson.Value {
	switch name {
	case "get_identity":
		return handlerArguments(`{}`)
	case "list_grants":
		return handlerArguments(`{"cursor":null}`)
	case "create_grant_request":
		return handlerArguments(`{"policy":{"scope":"tool","target":"sample.echo","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false}}`)
	case "get_grant_request", "cancel_grant_request":
		return handlerArguments(`{"id":"01J60000000000000000000001"}`)
	default:
		return handlerArguments(`{"cursor":null,"state":null}`)
	}
}

func handlerArguments(raw string) strictjson.Value {
	value, err := strictjson.ParseValue([]byte(raw), strictjson.Options{MaxBytes: 1 << 20, MaxDepth: 64})
	if err != nil {
		panic(err)
	}
	return value
}

func validGrantCursor(t *testing.T, codec *CursorCodec, subject authorization.AdmittedSubject) string {
	t.Helper()
	cursor, err := codec.EncodeGrantCursor(subject, authorization.SelfGrantCursor{Upper: 2, After: 1, AfterID: selfserviceID(700)})
	require.NoError(t, err)
	return cursor
}

func assertStructuredOutcome(t *testing.T, result invocation.LocalCallResult, want contract.CursorOutcome) {
	t.Helper()
	outcome := invocation.SanitizeLocalCallResult(result)
	require.NotNil(t, outcome.Result)
	var structured struct {
		Outcome contract.CursorOutcome `json:"outcome"`
	}
	require.NoError(t, json.Unmarshal(outcome.Result.StructuredContent, &structured))
	assert.Equal(t, want, structured.Outcome)
}
