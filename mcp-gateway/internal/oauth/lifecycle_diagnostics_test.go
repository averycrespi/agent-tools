package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/stretchr/testify/require"
)

func TestForegroundOAuthDiagnosticsObserveRealServiceOutcomes(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		store := &callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}
		bundle := callbackBundle("", false, contract.TokenEndpointAuthNone)
		bundle.graph.AuthorizationEndpoint = "https://issuer.example/authorize"
		requester := &tokenRequester{status: status, header: http.Header{"Content-Type": {contract.MediaTypeJSON}}, body: []byte(`{"access_token":"PRIVATE-token-canary","token_type":"Bearer","expires_in":3600}`)}
		secrets := &tokenSecrets{}
		service := newFlowService(store, flowResolverFake{graph: bundle.graph}, &flowRegistrarFake{registration: bundle.registration}, zeroReader{}, bundle.registration.CallbackURL, func() time.Time { return flowTime })
		service.configureCallback(requester, secrets, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
		var output bytes.Buffer
		adapter := diagnostics.New(&output, diagnostics.Debug)
		service.SetDiagnostics(adapter, func(string) uint64 { return 7 })
		t.Cleanup(func() { adapter.Finish(nil) })
		manager, err := runtimes.New(runtimes.Options{Repository: &oauthDiagnosticRuntimeRepository{serverID: store.created.Flow.ServerID}, Authority: oauthDiagnosticAuthority{secrets: secrets}, Driver: oauthDiagnosticDriver{}, Catalog: oauthDiagnosticCatalog{}, Diagnostics: adapter, DiagnosticReference: func(string) uint64 { return 7 }})
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		manager.Trigger(store.created.Flow.ServerID, nil, true)
		require.True(t, manager.Wait(ctx))
		require.Equal(t, contract.RuntimeAuthenticationRequired, manager.Status(store.created.Flow.ServerID).State)
		creation, err := service.Create(t.Context(), FlowRequest{ServerID: store.created.Flow.ServerID, ExpectedDesiredRevision: "1"})
		require.NoError(t, err)
		parsed, err := url.Parse(creation.AuthorizationURL)
		require.NoError(t, err)
		query := url.Values{"state": {parsed.Query().Get("state")}, "code": {"PRIVATE-code-canary"}}.Encode()
		result := service.HandleCallback(t.Context(), query)
		if status == http.StatusOK {
			require.Equal(t, CallbackSucceeded, result.Outcome)
			require.NotEmpty(t, secrets.secret)
		} else {
			require.Equal(t, CallbackInvalid, result.Outcome)
			require.Empty(t, secrets.secret)
		}
		require.Len(t, requester.requests, 1)
		require.Equal(t, CallbackInvalid, service.HandleCallback(t.Context(), query).Outcome)
		require.Len(t, requester.requests, 1, "diagnostics do not enable replay")
		manager.Trigger(store.created.Flow.ServerID, nil, true)
		require.True(t, manager.Wait(ctx))
		if status == http.StatusOK {
			require.Equal(t, contract.RuntimeActive, manager.Status(store.created.Flow.ServerID).State)
		} else {
			require.Equal(t, contract.RuntimeAuthenticationRequired, manager.Status(store.created.Flow.ServerID).State)
		}
		<-manager.Drain(ctx)
		service.Shutdown()
		require.True(t, adapter.Finish(nil))
		var ref uint64
		var authRequired, required, completed, failed, recovered bool
		for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'}) {
			var record struct {
				Event    string `json:"event"`
				Upstream uint64 `json:"upstream_ref"`
				Attempt  uint64 `json:"attempt_ref"`
				Phase    string `json:"phase"`
				Reason   string `json:"reason"`
			}
			require.NoError(t, json.Unmarshal(line, &record))
			require.EqualValues(t, 7, record.Upstream)
			if record.Event == "upstream_unhealthy" {
				authRequired = true
			}
			if record.Event == "upstream_recovered" {
				require.True(t, completed)
				recovered = true
			}
			if len(record.Event) < 6 || record.Event[:6] != "oauth_" {
				continue
			}
			if ref == 0 {
				ref = record.Attempt
			}
			require.Equal(t, ref, record.Attempt)
			switch record.Event {
			case "oauth_required":
				require.True(t, authRequired)
				required = true
			case "oauth_completed":
				completed = true
			case "oauth_failed":
				failed = true
				require.Equal(t, "oauth_exchange", record.Phase)
				require.Equal(t, "authentication_rejected", record.Reason)
			}
		}
		require.NotZero(t, ref)
		require.True(t, required)
		require.Equal(t, status == http.StatusOK, completed)
		require.Equal(t, status == http.StatusOK, recovered)
		require.Equal(t, status != http.StatusOK, failed)
		for _, canary := range []string{"PRIVATE-token-canary", "PRIVATE-code-canary", parsed.Query().Get("state"), parsed.Query().Get("code_challenge"), bundle.serverID, bundle.flowID, "https://issuer.example", "client-id"} {
			require.NotEmpty(t, canary)
			require.NotContains(t, output.String(), canary)
		}
	}
}

type expiringDiagnosticStore struct {
	flowStoreFake
	expired []string
}

func (store *expiringDiagnosticStore) ExpireAuthFlows(context.Context) ([]string, error) {
	expired := store.expired
	store.expired = nil
	return expired, nil
}

func TestOAuthExpiryDiagnosticRequiresObservedExpiration(t *testing.T) {
	store := &expiringDiagnosticStore{}
	service := newFlowService(store, flowResolverFake{}, &flowRegistrarFake{}, zeroReader{}, "http://127.0.0.1:8210/oauth/callback", func() time.Time { return flowTime })
	bundle := callbackBundle("private-expiring-state", false, contract.TokenEndpointAuthNone)
	service.byState[bundle.state] = bundle
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	service.SetDiagnostics(adapter, func(string) uint64 { return 9 })
	require.NoError(t, service.expire(t.Context()))
	require.Len(t, service.byState, 1)
	store.expired = []string{bundle.flowID}
	require.NoError(t, service.expire(t.Context()))
	require.Empty(t, service.byState)
	require.NoError(t, service.expire(t.Context()))
	require.True(t, adapter.Finish(nil))
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte(`"event":"oauth_expired"`)))
	require.NotContains(t, output.String(), bundle.state)
}
