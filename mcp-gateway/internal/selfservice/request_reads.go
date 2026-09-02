package selfservice

import (
	"context"
	"errors"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
)

var ErrInvalidSubject = errors.New("self-service admitted subject is invalid")

// SubjectAuthority verifies that a sealed subject belongs to the composed authority owner.
type SubjectAuthority interface {
	OwnsAdmittedSubject(authorization.AdmittedSubject) bool
}

// RequestReader is the owner-scoped request projection consumed by self-service.
type RequestReader interface {
	GetOwned(context.Context, string, string) (contract.AgentGrantRequest, bool, error)
	ListOwned(context.Context, string, *contract.GrantRequestState, *grantrequests.SelfCursor, int) (grantrequests.SelfPage, error)
}

// RequestReadService owns agent request outcomes and protected cursor handling.
type RequestReadService struct {
	authority SubjectAuthority
	reader    RequestReader
	cursors   *CursorCodec
}

// NewRequestReadService binds one authority, request owner, and process-local cursor authority.
func NewRequestReadService(authority SubjectAuthority, reader RequestReader, cursors *CursorCodec) (*RequestReadService, error) {
	if authority == nil || reader == nil || cursors == nil {
		return nil, errors.New("request read service dependencies are incomplete")
	}
	return &RequestReadService{authority: authority, reader: reader, cursors: cursors}, nil
}

// GetRequest returns a non-enumerating owner-only request result.
func (service *RequestReadService) GetRequest(ctx context.Context, subject authorization.AdmittedSubject, requestID string) (contract.GetGrantRequestResult, error) {
	if service == nil || service.authority == nil || !service.authority.OwnsAdmittedSubject(subject) {
		return contract.GetGrantRequestResult{}, ErrInvalidSubject
	}
	request, found, err := service.reader.GetOwned(ctx, subject.PrincipalID(), requestID)
	if err != nil {
		return contract.GetGrantRequestResult{}, err
	}
	if !found {
		return contract.GetGrantRequestResult{Outcome: contract.RequestNotFound}, nil
	}
	return contract.GetGrantRequestResult{Outcome: contract.RequestFound, Request: &request}, nil
}

// ListRequests returns an owner-only page or one closed cursor outcome.
func (service *RequestReadService) ListRequests(
	ctx context.Context,
	subject authorization.AdmittedSubject,
	input contract.ListGrantRequestsInput,
) (contract.ListGrantRequestsResult, error) {
	if service == nil || service.authority == nil || service.reader == nil || service.cursors == nil ||
		!service.authority.OwnsAdmittedSubject(subject) {
		return contract.ListGrantRequestsResult{}, ErrInvalidSubject
	}
	if _, valid := encodeRequestFilter(input.State); !valid {
		return contract.ListGrantRequestsResult{}, grantrequests.ErrInvalidInput
	}
	var cursor *grantrequests.SelfCursor
	if input.Cursor != nil {
		decoded, outcome, err := service.cursors.DecodeRequestCursor(*input.Cursor, subject, input.State)
		if err != nil {
			return contract.ListGrantRequestsResult{}, err
		}
		if outcome != contract.CursorOK {
			return closedCursorResult(outcome), nil
		}
		cursor = &decoded
	}
	page, err := service.reader.ListOwned(ctx, subject.PrincipalID(), input.State, cursor, int(mustSelfServiceLimit("agent_self_service_list_page")))
	if errors.Is(err, grantrequests.ErrStaleCursor) {
		return closedCursorResult(contract.CursorStale), nil
	}
	if err != nil {
		return contract.ListGrantRequestsResult{}, err
	}
	items := page.Items
	if items == nil {
		items = []contract.AgentGrantRequest{}
	}
	result := contract.ListGrantRequestsResult{Outcome: contract.CursorOK, Items: items}
	if page.Next != nil {
		encoded, err := service.cursors.EncodeRequestCursor(subject, input.State, *page.Next)
		if err != nil {
			return contract.ListGrantRequestsResult{}, err
		}
		result.NextCursor = &encoded
	}
	return result, nil
}

func closedCursorResult(outcome contract.CursorOutcome) contract.ListGrantRequestsResult {
	return contract.ListGrantRequestsResult{Outcome: outcome, Items: []contract.AgentGrantRequest{}}
}

var (
	_ SubjectAuthority = (*authorization.Repository)(nil)
	_ RequestReader    = (*grantrequests.Repository)(nil)
)
