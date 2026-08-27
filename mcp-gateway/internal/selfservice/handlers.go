package selfservice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type RequestRepository interface {
	RequestReader
	CreateOrExisting(context.Context, grantrequests.CreateRequest) (contract.CreateGrantRequestResult, error)
	CancelOwned(context.Context, string, string) (contract.CancelGrantRequestResult, error)
}

type Service struct {
	authority   SubjectAuthority
	projections ProjectionReader
	requests    RequestRepository
	reads       *RequestReadService
	cursors     *CursorCodec
	targets     map[string]invocation.LocalTarget
}

func NewService(authority SubjectAuthority, projections ProjectionReader, requests RequestRepository, cursors *CursorCodec) (*Service, error) {
	if authority == nil || projections == nil || requests == nil || cursors == nil {
		return nil, errors.New("self-service dependencies are incomplete")
	}
	reads, err := NewRequestReadService(authority, requests, cursors)
	if err != nil {
		return nil, err
	}
	service := &Service{
		authority: authority, projections: projections, requests: requests, reads: reads, cursors: cursors,
		targets: make(map[string]invocation.LocalTarget, 6),
	}
	for _, tool := range contract.SyntheticSelfServiceTools() {
		synthetic, found := catalog.ResolveSyntheticCall(tool.ExternalName)
		if !found {
			return nil, errors.New("self-service synthetic catalog is unavailable")
		}
		handler, validator := service.handler(tool.UpstreamName)
		target, targetErr := invocation.NewLocalTargetWithValidation(synthetic, validator, handler)
		if targetErr != nil {
			return nil, targetErr
		}
		service.targets[tool.ExternalName] = target
	}
	return service, nil
}

func (service *Service) Resolve(externalName string) (invocation.LocalTarget, bool) {
	if service == nil {
		return invocation.LocalTarget{}, false
	}
	target, found := service.targets[externalName]
	return target, found
}

func (service *Service) handler(name string) (invocation.LocalHandler, func(strictjson.Value) error) {
	switch name {
	case "get_identity":
		return service.handleGetIdentity, validateEmptyInput
	case "list_grants":
		return service.handleListGrants, validateListGrantsInput
	case "create_grant_request":
		return service.handleCreateGrantRequest, validateCreateRequestInput
	case "get_grant_request":
		return service.handleGetGrantRequest, validateRequestIDInput
	case "list_grant_requests":
		return service.handleListGrantRequests, validateListRequestsInput
	case "cancel_grant_request":
		return service.handleCancelGrantRequest, validateRequestIDInput
	default:
		return nil, nil
	}
}

func (service *Service) handleGetIdentity(ctx context.Context, subject authorization.AdmittedSubject, _ strictjson.Value) invocation.LocalCallResult {
	if !service.validSubject(subject) {
		return invocation.LocalStorageFailure(false)
	}
	identity, err := service.projections.ReadSelfIdentity(ctx, subject)
	if err != nil {
		return localFailure(err)
	}
	return localSuccess(contract.SummaryIdentityReturned, contract.GetIdentityResult{Identity: identity})
}

func (service *Service) handleListGrants(ctx context.Context, subject authorization.AdmittedSubject, arguments strictjson.Value) invocation.LocalCallResult {
	if !service.validSubject(subject) {
		return invocation.LocalStorageFailure(false)
	}
	var input contract.ListGrantsInput
	if err := decodeArguments(arguments, &input); err != nil {
		return localFailure(err)
	}
	var cursor *authorization.SelfGrantCursor
	if input.Cursor != nil {
		decoded, outcome, err := service.cursors.DecodeGrantCursor(*input.Cursor, subject)
		if err != nil {
			return localFailure(err)
		}
		if outcome != contract.CursorOK {
			return localSuccess(contract.SummaryGrantsReturned, contract.ListGrantsResult{Outcome: outcome, Items: []contract.AgentGrant{}})
		}
		cursor = &decoded
	}
	page, err := service.projections.ListSelfGrants(ctx, subject, cursor, int(mustSelfServiceLimit("agent_self_service_list_page")))
	if errors.Is(err, authorization.ErrStaleCursor) {
		return localSuccess(contract.SummaryGrantsReturned, contract.ListGrantsResult{Outcome: contract.CursorStale, Items: []contract.AgentGrant{}})
	}
	if err != nil {
		return localFailure(err)
	}
	items := page.Items
	if items == nil {
		items = []contract.AgentGrant{}
	}
	result := contract.ListGrantsResult{Outcome: contract.CursorOK, Items: items}
	if page.Next != nil {
		encoded, encodeErr := service.cursors.EncodeGrantCursor(subject, *page.Next)
		if encodeErr != nil {
			return localFailure(encodeErr)
		}
		result.NextCursor = &encoded
	}
	return localSuccess(contract.SummaryGrantsReturned, result)
}

func (service *Service) handleCreateGrantRequest(ctx context.Context, subject authorization.AdmittedSubject, arguments strictjson.Value) invocation.LocalCallResult {
	if !service.validSubject(subject) {
		return invocation.LocalStorageFailure(false)
	}
	var input contract.CreateGrantRequestInput
	if err := decodeArguments(arguments, &input); err != nil {
		return localFailure(err)
	}
	result, err := service.requests.CreateOrExisting(ctx, grantrequests.CreateRequest{PrincipalID: subject.PrincipalID(), Policy: input.Policy})
	if err != nil {
		return localFailure(err)
	}
	if !validCreateResult(result) {
		return invocation.LocalStorageFailure(false)
	}
	return localSuccess(contract.SummaryGrantRequestProcessed, result)
}

func (service *Service) handleGetGrantRequest(ctx context.Context, subject authorization.AdmittedSubject, arguments strictjson.Value) invocation.LocalCallResult {
	if !service.validSubject(subject) {
		return invocation.LocalStorageFailure(false)
	}
	var input contract.GrantRequestIDInput
	if err := decodeArguments(arguments, &input); err != nil {
		return localFailure(err)
	}
	result, err := service.reads.GetRequest(ctx, subject, input.ID)
	if err != nil {
		return localFailure(err)
	}
	if !validGetResult(result) {
		return invocation.LocalStorageFailure(false)
	}
	return localSuccess(contract.SummaryGrantRequestReturned, result)
}

func (service *Service) handleListGrantRequests(ctx context.Context, subject authorization.AdmittedSubject, arguments strictjson.Value) invocation.LocalCallResult {
	if !service.validSubject(subject) {
		return invocation.LocalStorageFailure(false)
	}
	var input contract.ListGrantRequestsInput
	if err := decodeArguments(arguments, &input); err != nil {
		return localFailure(err)
	}
	result, err := service.reads.ListRequests(ctx, subject, input)
	if err != nil {
		return localFailure(err)
	}
	if !validRequestListResult(result) {
		return invocation.LocalStorageFailure(false)
	}
	return localSuccess(contract.SummaryGrantRequestsReturned, result)
}

func (service *Service) handleCancelGrantRequest(ctx context.Context, subject authorization.AdmittedSubject, arguments strictjson.Value) invocation.LocalCallResult {
	if !service.validSubject(subject) {
		return invocation.LocalStorageFailure(false)
	}
	var input contract.GrantRequestIDInput
	if err := decodeArguments(arguments, &input); err != nil {
		return localFailure(err)
	}
	result, err := service.requests.CancelOwned(ctx, subject.PrincipalID(), input.ID)
	if err != nil {
		return localFailure(err)
	}
	if !validCancelResult(result) {
		return invocation.LocalStorageFailure(false)
	}
	return localSuccess(contract.SummaryGrantRequestCancellationProcessed, result)
}

func (service *Service) validSubject(subject authorization.AdmittedSubject) bool {
	return service != nil && service.authority != nil && service.authority.OwnsAdmittedSubject(subject)
}

func validCreateResult(result contract.CreateGrantRequestResult) bool {
	if _, err := contract.ParseCreateGrantRequestOutcome(string(result.Outcome)); err != nil {
		return false
	}
	needsRequest := result.Outcome == contract.RequestCreated || result.Outcome == contract.RequestExisting
	return (result.Request != nil) == needsRequest
}

func validGetResult(result contract.GetGrantRequestResult) bool {
	if _, err := contract.ParseGetGrantRequestOutcome(string(result.Outcome)); err != nil {
		return false
	}
	return (result.Request != nil) == (result.Outcome == contract.RequestFound)
}

func validRequestListResult(result contract.ListGrantRequestsResult) bool {
	if _, err := contract.ParseCursorOutcome(string(result.Outcome)); err != nil {
		return false
	}
	return result.Items != nil && (result.Outcome == contract.CursorOK || len(result.Items) == 0 && result.NextCursor == nil)
}

func validCancelResult(result contract.CancelGrantRequestResult) bool {
	if _, err := contract.ParseCancelGrantRequestOutcome(string(result.Outcome)); err != nil {
		return false
	}
	return (result.Request == nil) == (result.Outcome == contract.RequestCancellationNotFound)
}

func localSuccess(summary string, structured any) invocation.LocalCallResult {
	result := struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Structured any `json:"structuredContent"`
	}{Structured: structured}
	result.Content = append(result.Content, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: summary})
	encoded, err := json.Marshal(result)
	if err != nil || int64(len(encoded)) > mustSelfServiceLimit("downstream_mcp_body_bytes") {
		return invocation.LocalStorageFailure(false)
	}
	return invocation.LocalSuccess(encoded)
}

func localFailure(err error) invocation.LocalCallResult {
	return invocation.LocalStorageFailure(errors.Is(err, grantrequests.ErrStorageOutcomeUncertain))
}

var _ RequestRepository = (*grantrequests.Repository)(nil)
