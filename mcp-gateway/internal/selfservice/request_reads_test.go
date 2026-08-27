package selfservice

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const selfserviceTestInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var selfserviceTestTime = time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC)

func TestS5RequestReadCursorSeamDerivesOwnerAndProtectsBoundaries(t *testing.T) {
	authority, store, subject, credential := newAdmittedSubject(t)
	reader := &fakeRequestReader{
		get:      contract.AgentGrantRequest{ID: selfserviceID(501), State: contract.RequestPending, Revision: "1"},
		getFound: true,
		pages: []grantrequests.SelfPage{
			{Items: []contract.AgentGrantRequest{{ID: selfserviceID(501), State: contract.RequestPending, Revision: "1"}}, Next: &grantrequests.SelfCursor{Upper: 9, After: 4, AfterID: selfserviceID(501)}},
			{Items: []contract.AgentGrantRequest{{ID: selfserviceID(502), State: contract.RequestApproved, Revision: "2"}}},
		},
	}
	codec, err := NewCursorCodec(bytes.NewReader(bytes.Repeat([]byte{0x41}, cursorKeyBytes)), selfserviceID(900))
	require.NoError(t, err)
	service, err := NewRequestReadService(authority, reader, codec)
	require.NoError(t, err)

	got, err := service.GetRequest(context.Background(), subject, selfserviceID(501))
	require.NoError(t, err)
	assert.Equal(t, contract.RequestFound, got.Outcome)
	require.NotNil(t, got.Request)
	assert.Equal(t, subject.PrincipalID(), reader.getPrincipal)
	reader.getFound = false
	notFound, err := service.GetRequest(context.Background(), subject, selfserviceID(999))
	require.NoError(t, err)
	assert.Equal(t, contract.RequestNotFound, notFound.Outcome)
	assert.Nil(t, notFound.Request)

	first, err := service.ListRequests(context.Background(), subject, contract.ListGrantRequestsInput{})
	require.NoError(t, err)
	assert.Equal(t, contract.CursorOK, first.Outcome)
	require.Len(t, first.Items, 1)
	require.NotNil(t, first.NextCursor)
	assert.LessOrEqual(t, len(*first.NextCursor), int(cursorMaximumBytes))
	second, err := service.ListRequests(context.Background(), subject, contract.ListGrantRequestsInput{Cursor: first.NextCursor})
	require.NoError(t, err)
	assert.Equal(t, contract.CursorOK, second.Outcome)
	require.Len(t, second.Items, 1)
	assert.Nil(t, second.NextCursor)
	require.Len(t, reader.listCursors, 2)
	assert.Nil(t, reader.listCursors[0])
	assert.Equal(t, grantrequests.SelfCursor{Upper: 9, After: 4, AfterID: selfserviceID(501)}, *reader.listCursors[1])
	assert.Equal(t, []string{subject.PrincipalID(), subject.PrincipalID()}, reader.listPrincipals)
	assert.Equal(t, []int{100, 100}, reader.listLimits)

	pending := contract.RequestPending
	staleFilter, err := service.ListRequests(context.Background(), subject, contract.ListGrantRequestsInput{Cursor: first.NextCursor, State: &pending})
	require.NoError(t, err)
	assert.Equal(t, contract.CursorStale, staleFilter.Outcome)
	assert.Empty(t, staleFilter.Items)
	assert.Nil(t, staleFilter.NextCursor)

	grantCursor, err := codec.EncodeGrantCursor(subject, authorization.SelfGrantCursor{Upper: 9, After: 4, AfterID: selfserviceID(501)})
	require.NoError(t, err)
	crossMethod, err := service.ListRequests(context.Background(), subject, contract.ListGrantRequestsInput{Cursor: &grantCursor})
	require.NoError(t, err)
	assert.Equal(t, contract.CursorStale, crossMethod.Outcome)

	tampered := *first.NextCursor
	if strings.HasSuffix(tampered, "A") {
		tampered = tampered[:len(tampered)-1] + "B"
	} else {
		tampered = tampered[:len(tampered)-1] + "A"
	}
	invalid, err := service.ListRequests(context.Background(), subject, contract.ListGrantRequestsInput{Cursor: &tampered})
	require.NoError(t, err)
	assert.Equal(t, contract.CursorInvalid, invalid.Outcome)
	assert.Empty(t, invalid.Items)

	_, err = service.ListRequests(context.Background(), subject, contract.ListGrantRequestsInput{Cursor: stringPointer("")})
	assert.ErrorIs(t, err, ErrMalformedCursor)
	malformed := "not-a-self-cursor"
	_, err = service.ListRequests(context.Background(), subject, contract.ListGrantRequestsInput{Cursor: &malformed})
	assert.ErrorIs(t, err, ErrMalformedCursor)

	restartedCodec, err := NewCursorCodec(bytes.NewReader(bytes.Repeat([]byte{0x41}, cursorKeyBytes)), selfserviceID(901))
	require.NoError(t, err)
	restarted, err := NewRequestReadService(authority, reader, restartedCodec)
	require.NoError(t, err)
	staleRestart, err := restarted.ListRequests(context.Background(), subject, contract.ListGrantRequestsInput{Cursor: first.NextCursor})
	require.NoError(t, err)
	assert.Equal(t, contract.CursorStale, staleRestart.Outcome)

	wrongKeyCodec, err := NewCursorCodec(bytes.NewReader(bytes.Repeat([]byte{0x42}, cursorKeyBytes)), selfserviceID(900))
	require.NoError(t, err)
	wrongKey, err := NewRequestReadService(authority, reader, wrongKeyCodec)
	require.NoError(t, err)
	invalidKey, err := wrongKey.ListRequests(context.Background(), subject, contract.ListGrantRequestsInput{Cursor: first.NextCursor})
	require.NoError(t, err)
	assert.Equal(t, contract.CursorInvalid, invalidKey.Outcome)

	replacement, err := authority.IssueCredential(context.Background(), credential.Principal.ID, credential.Principal.Revision)
	require.NoError(t, err)
	replacementSubject := admitSubject(t, authority, store, replacement.Bearer)
	staleCredential, err := service.ListRequests(context.Background(), replacementSubject, contract.ListGrantRequestsInput{Cursor: first.NextCursor})
	require.NoError(t, err)
	assert.Equal(t, contract.CursorStale, staleCredential.Outcome)
	reader.getFound = true
	ownedAfterRotation, err := service.GetRequest(context.Background(), subject, selfserviceID(501))
	require.NoError(t, err, "detached ownership remains stable across credential replacement")
	assert.Equal(t, contract.RequestFound, ownedAfterRotation.Outcome)
}

func TestS5RequestCursorRejectsInvalidConstructionAndAuthenticatedState(t *testing.T) {
	_, _, subject, _ := newAdmittedSubject(t)
	_, err := NewCursorCodec(nil, selfserviceID(900))
	require.Error(t, err)
	_, err = NewCursorCodec(bytes.NewReader(make([]byte, cursorKeyBytes-1)), selfserviceID(900))
	require.Error(t, err)
	_, err = NewCursorCodec(bytes.NewReader(make([]byte, cursorKeyBytes)), "malformed")
	require.Error(t, err)

	codec, err := NewCursorCodec(bytes.NewReader(make([]byte, cursorKeyBytes)), selfserviceID(900))
	require.NoError(t, err)
	_, err = codec.EncodeRequestCursor(subject, nil, grantrequests.SelfCursor{})
	assert.ErrorIs(t, err, ErrInvalidCursorState)
	valid, err := codec.EncodeRequestCursor(subject, nil, grantrequests.SelfCursor{Upper: 2, After: 1, AfterID: selfserviceID(501)})
	require.NoError(t, err)
	frame, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(valid, cursorPrefix))
	require.NoError(t, err)
	frame[offsetCursorVersion] = 2
	copy(frame[cursorPayloadBytes:], codec.authenticate(cursorMethodRequests, frame[:cursorPayloadBytes]))
	authenticatedInvalid := cursorPrefix + base64.RawURLEncoding.EncodeToString(frame)
	_, outcome, err := codec.DecodeRequestCursor(authenticatedInvalid, subject, nil)
	require.NoError(t, err)
	assert.Equal(t, contract.CursorInvalid, outcome)

	value := cursorPrefix + strings.Repeat("A", int(cursorMaximumBytes))
	_, outcome, err = codec.DecodeRequestCursor(value, subject, nil)
	assert.ErrorIs(t, err, ErrMalformedCursor)
	assert.Empty(t, outcome)
}

type fakeRequestReader struct {
	get            contract.AgentGrantRequest
	getFound       bool
	getPrincipal   string
	pages          []grantrequests.SelfPage
	listPrincipals []string
	listCursors    []*grantrequests.SelfCursor
	listLimits     []int
}

func (reader *fakeRequestReader) GetOwned(_ context.Context, principalID, _ string) (contract.AgentGrantRequest, bool, error) {
	reader.getPrincipal = principalID
	return reader.get, reader.getFound, nil
}

func (reader *fakeRequestReader) ListOwned(_ context.Context, principalID string, _ *contract.GrantRequestState, cursor *grantrequests.SelfCursor, limit int) (grantrequests.SelfPage, error) {
	reader.listPrincipals = append(reader.listPrincipals, principalID)
	if cursor == nil {
		reader.listCursors = append(reader.listCursors, nil)
	} else {
		copied := *cursor
		reader.listCursors = append(reader.listCursors, &copied)
	}
	reader.listLimits = append(reader.listLimits, limit)
	if len(reader.pages) == 0 {
		return grantrequests.SelfPage{Items: []contract.AgentGrantRequest{}}, nil
	}
	page := reader.pages[0]
	reader.pages = reader.pages[1:]
	return page, nil
}

type selfserviceClock struct{ now time.Time }

func (clock selfserviceClock) Now() time.Time { return clock.now }

func newAdmittedSubject(t *testing.T) (*authorization.Repository, *storage.Store, authorization.AdmittedSubject, contract.AgentCredentialCreation) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, selfserviceTestInstallationID)
	require.NoError(t, err)
	authorityEntropy := make([]byte, 512)
	for index := range authorityEntropy {
		authorityEntropy[index] = byte(index%251 + 1)
	}
	authority, err := authorization.New(store, selfserviceClock{now: selfserviceTestTime}, bytes.NewReader(authorityEntropy))
	require.NoError(t, err)
	principal, err := authority.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{
		DisplayName: "Agent", Visibility: contract.VisibilityRequestable,
	})
	require.NoError(t, err)
	credential, err := authority.IssueCredential(context.Background(), principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	subject := admitSubject(t, authority, store, credential.Bearer)
	t.Cleanup(func() { _ = store.Close(); _ = ownership.Close() })
	return authority, store, subject, credential
}

func admitSubject(t *testing.T, authority *authorization.Repository, store *storage.Store, bearer string) authorization.AdmittedSubject {
	t.Helper()
	lease, err := authority.Authenticate(context.Background(), bearer)
	require.NoError(t, err)
	var pending *authorization.PendingDetachment
	require.NoError(t, authority.WithAdmission(context.Background(), lease, func(admission *authorization.Admission) error {
		if mutationErr := store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			result, token, phase, verifyErr := admission.VerifyResolvedTx(context.Background(), transaction, authorization.ResolvedVerification{
				ServerID: contract.SyntheticServerID, UpstreamName: "get_identity",
				Arguments: strictjson.Value{Type: strictjson.ValueObject},
			})
			if verifyErr != nil {
				return verifyErr
			}
			if result.Decision != contract.DecisionAllow || phase != authorization.ResolvedEvaluated || token == nil {
				return errors.New("test admission was not allowed")
			}
			pending = token
			return nil
		}); mutationErr != nil {
			return mutationErr
		}
		return pending.CommitSucceeded()
	}))
	subject, err := pending.Subject()
	require.NoError(t, err)
	return subject
}

func selfserviceID(value int) string {
	return "01J70000000000000000000" + string(rune('0'+value/100)) + string(rune('0'+value/10%10)) + string(rune('0'+value%10))
}

func stringPointer(value string) *string { return &value }
