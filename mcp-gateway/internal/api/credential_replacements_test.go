package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	serverdomain "github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialReplacementIsWriteOnlyAndReturnsSafeOperation(t *testing.T) {
	service := &replacementServiceFake{}
	var invalidations []contract.Invalidation
	var triggered string
	handler := New(Options{Replacements: service, Invalidate: func(value contract.Invalidation) { invalidations = append(invalidations, value) }, TriggerServer: func(_ string, operationID *string, _ bool) { triggered = *operationID }})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/credential-replacements", strings.NewReader(`{"kind":"static_credential","expected_revision":"0","values":{"token":"replacement-canary"}}`))
	request.Header.Set("Content-Type", contract.MediaTypeJSON)
	request.Header.Set("If-Match", `"server-01ARZ3NDEKTSV4RRFFQ69G5FAV-1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, http.StatusAccepted, response.Code)
	assert.NotContains(t, response.Body.String(), "replacement-canary")
	assert.Equal(t, []string{"token"}, service.prepared.Slots)
	generation, err := servercredentials.DecodeStaticGeneration(service.secret)
	require.NoError(t, err)
	assert.Equal(t, "replacement-canary", generation.Values["token"])
	assert.Equal(t, service.operation.ID, triggered)
	assert.Len(t, invalidations, 4)
	assert.NotContains(t, response.Header().Get("ETag"), "server-")
}

func TestCredentialReplacementLostResponseStillTriggersObservableOperation(t *testing.T) {
	service := &replacementServiceFake{replaceErr: errors.New("acknowledgement lost")}
	triggered := false
	handler := New(Options{Replacements: service, TriggerServer: func(string, *string, bool) { triggered = true }})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/credential-replacements", strings.NewReader(`{"kind":"static_credential","expected_revision":"0","values":{"token":"replacement-canary"}}`))
	request.Header.Set("Content-Type", contract.MediaTypeJSON)
	request.Header.Set("If-Match", `"server-01ARZ3NDEKTSV4RRFFQ69G5FAV-1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.True(t, triggered)
	assert.NotContains(t, response.Body.String(), "acknowledgement lost")
	assert.NotContains(t, response.Body.String(), "replacement-canary")
}

func TestCredentialReplacementRejectsWrongUnionBeforeService(t *testing.T) {
	service := &replacementServiceFake{}
	handler := New(Options{Replacements: service})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/credential-replacements", strings.NewReader(`{"kind":"oauth_client","expected_revision":"0","values":{},"client_secret":"secret"}`))
	request.Header.Set("Content-Type", contract.MediaTypeJSON)
	request.Header.Set("If-Match", `"server-01ARZ3NDEKTSV4RRFFQ69G5FAV-1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Zero(t, service.prepareCalls)
}

type replacementServiceFake struct {
	prepared     serverdomain.CredentialReplacementRequest
	prepareCalls int
	secret       []byte
	operation    serverdomain.Operation
	replaceErr   error
}

func (service *replacementServiceFake) Prepare(_ context.Context, request serverdomain.CredentialReplacementRequest) (serverdomain.CredentialReplacementPlan, error) {
	service.prepareCalls++
	service.prepared = request
	return serverdomain.CredentialReplacementPlan{Fence: serverdomain.CredentialFence{ServerID: request.ServerID, Kind: request.Kind, ExpectedDesiredRevision: request.ExpectedDesiredRevision, ExpectedCredentialRevision: request.ExpectedCredentialRevision}, OperationID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Slots: request.Slots}, nil
}

func (service *replacementServiceFake) Replace(_ context.Context, plan serverdomain.CredentialReplacementPlan, secret []byte) (serverdomain.CredentialReplacementPublication, error) {
	service.secret = append([]byte(nil), secret...)
	service.operation = serverdomain.Operation{ID: plan.OperationID, ServerID: plan.Fence.ServerID, Kind: contract.OperationCredentialReplace, TargetDesiredRevision: "1", TargetCredentialRevisions: contract.CredentialRevisions{StaticCredential: "1", OAuthClient: "0", OAuthTokens: "0"}, State: contract.OperationScheduled, CreatedAt: "2026-08-22T12:00:00Z"}
	return serverdomain.CredentialReplacementPublication{Revision: "1", Operation: service.operation}, service.replaceErr
}
