package oauth

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestTokenPredicateReachesCallbackDefaultSinkWithoutReplay(t *testing.T) {
	for _, body := range []string{`{"access_token":"token-canary","token_type":"Bearer","expires_in":-1}`, `{"access_token":"token-canary","token_type":"Bearer","expires_in":1.5}`} {
		store := &callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}
		requester := &tokenRequester{status: 200, header: http.Header{"Content-Type": {contract.MediaTypeJSON}}, body: []byte(body)}
		secrets := new(tokenSecrets)
		service := callbackService(store, requester, secrets)
		bundle := callbackBundle("state-canary", false, contract.TokenEndpointAuthNone)
		service.byState[bundle.state] = bundle
		var output bytes.Buffer
		adapter := diagnostics.New(&output, diagnostics.Warn)
		t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
		service.SetDiagnostics(adapter, nil)
		result := service.HandleCallback(t.Context(), "state=state-canary&code=code-canary")
		require.Equal(t, CallbackInvalid, result.Outcome)
		require.Equal(t, CallbackInvalid, service.HandleCallback(t.Context(), "state=state-canary&code=code-canary").Outcome)
		require.Len(t, requester.requests, 1)
		require.Empty(t, secrets.secret)
		require.True(t, adapter.Finish(nil))
		require.Contains(t, output.String(), "field=expires_in rule=bounded_nonnegative_integer")
		for _, secret := range []string{"token-canary", "state-canary", "code-canary", "verifier-value"} {
			require.NotContains(t, output.String(), secret)
		}
	}
}

func TestMetadataPredicateReachesForegroundDefaultSink(t *testing.T) {
	for _, test := range []struct {
		response fetchResponse
		want     string
	}{
		{fetchResponse{status: 200, contentType: "text/plain", body: "private-body-canary"}, "rule=json_media_type"},
		{fetchResponse{status: 200, body: `{"resource":"https://resource.example/mcp","authorization_servers":17,"private-member-canary":"actual-token-canary"}`}, "field=authorization_servers rule=nonempty_unique_string_array"},
	} {
		fetch := &scriptedFetch{responses: map[string]fetchResponse{"https://resource.example/.well-known/oauth-protected-resource/mcp": test.response}}
		store := &callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}
		service := newFlowService(store, newResolver(fetch), &flowRegistrarFake{}, zeroReader{}, "http://127.0.0.1:8210/oauth/callback", func() time.Time { return flowTime })
		var output bytes.Buffer
		adapter := diagnostics.New(&output, diagnostics.Warn)
		t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
		service.SetDiagnostics(adapter, nil)
		_, err := service.Create(t.Context(), FlowRequest{ServerID: store.created.Flow.ServerID, ExpectedDesiredRevision: "1"})
		require.Error(t, err)
		service.Shutdown()
		require.True(t, adapter.Finish(nil))
		require.Len(t, fetch.requests, 1, "malformed metadata must not enable fallback")
		require.Contains(t, output.String(), test.want)
		require.Contains(t, output.String(), "role=protected_resource method=GET attempt=1 http_status=200")
		for _, secret := range []string{"private-body-canary", "private-member-canary", "actual-token-canary"} {
			require.NotContains(t, output.String(), secret)
		}
	}
}
