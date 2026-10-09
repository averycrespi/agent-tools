package oauth

import (
	"bytes"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestDynamicRegistrationLocalFailurePreservesRemoteSuccessAtDefaultSink(t *testing.T) {
	for _, mode := range []string{"write", "audit"} {
		t.Run(mode, func(t *testing.T) {
			graph := registrationGraph([]string{"client_secret_basic"})
			requester := &registrationRequester{status: 201, header: http.Header{"Content-Type": {contract.MediaTypeJSON}}, body: dynamicResponseJSON("client", contract.TokenEndpointAuthClientSecretBasic, "actual-client-secret-canary", 0)}
			store := &registrationStoreFake{}
			secrets := &secretPublisherFake{}
			if mode == "write" {
				secrets.err = errors.New("encrypted write: permission denied actual-client-secret-canary")
			} else {
				store.auditHook = func(event contract.AuditEvent) error {
					if event.Phase == "outcome" {
						return errors.New("audit disk full actual-client-secret-canary")
					}
					return nil
				}
			}
			registrar := newRegistrar(requester, store, secrets, "01ARZ3NDEKTSV4RRFFQ69G5FAV", func() time.Time { return flowTime }, func() bool { return true })
			flows := &callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}
			service := newFlowService(flows, flowResolverFake{graph: graph}, registrar, zeroReader{}, "http://127.0.0.1:8210/oauth/callback", func() time.Time { return flowTime })
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
			service.SetDiagnostics(adapter, nil)
			_, err := service.Create(t.Context(), FlowRequest{ServerID: flows.created.Flow.ServerID, ExpectedDesiredRevision: "1"})
			require.Error(t, err)
			service.Shutdown()
			require.True(t, adapter.Finish(nil))
			require.Len(t, requester.requests, 1)
			require.Equal(t, 1, secrets.calls)
			require.Contains(t, output.String(), "remote_registration=validated_HTTP_201")
			require.NotContains(t, output.String(), "actual-client-secret-canary")
			if mode == "write" {
				require.Contains(t, output.String(), "permission denied")
				require.Contains(t, output.String(), "local_publication=not_ack")
			} else {
				require.Contains(t, output.String(), "audit disk full")
				require.Contains(t, output.String(), "local_publication=ack_revision_1")
			}
		})
	}
}
