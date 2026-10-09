package oauth

import (
	"bytes"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestDynamicRegistrationLocalFailurePreservesRemoteSuccessAtDefaultSink(t *testing.T) {
	for _, mode := range []string{"write", "post_commit", "storage_uncertain", "audit"} {
		t.Run(mode, func(t *testing.T) {
			graph := registrationGraph([]string{"client_secret_basic"})
			requester := &registrationRequester{status: 201, header: http.Header{"Content-Type": {contract.MediaTypeJSON}}, body: dynamicResponseJSON("client", contract.TokenEndpointAuthClientSecretBasic, "actual-client-secret-canary", 0)}
			store := &registrationStoreFake{}
			secrets := &secretPublisherFake{}
			switch mode {
			case "write":
				secrets.err = errors.New("encrypted write: permission denied actual-client-secret-canary")
			case "post_commit":
				secrets.errorResult = keyring.CutoverResult{Revision: "7"}
				secrets.err = errors.New("post-commit settlement refused actual-client-secret-canary")
			case "storage_uncertain":
				secrets.errorResult = keyring.CutoverResult{Revision: "8"}
				secrets.err = errors.Join(storage.ErrStorageLatched, errors.New("directory synchronization refused actual-client-secret-canary"))
			default:
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
			switch mode {
			case "write":
				require.Contains(t, output.String(), "permission denied")
				require.Contains(t, output.String(), "local_publication=not_ack")
			case "post_commit":
				require.Contains(t, output.String(), "post-commit settlement refused")
				require.Contains(t, output.String(), "local_publication=ack_revision_7")
			case "storage_uncertain":
				require.Contains(t, output.String(), "directory synchronization refused")
				require.Contains(t, output.String(), "local_publication=unknown_returned_revision_8")
			default:
				require.Contains(t, output.String(), "audit disk full")
				require.Contains(t, output.String(), "local_publication=ack_revision_1")
			}
		})
	}
}
