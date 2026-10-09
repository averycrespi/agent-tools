package oauth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/stretchr/testify/require"
)

type diagnosticRefreshOperation struct {
	refreshOperationFake
	clientErr       error
	invalidationErr error
}

func (op *diagnosticRefreshOperation) ReadActive(ctx context.Context, ns keyring.Namespace) ([]byte, keyring.CutoverResult, error) {
	if ns.Kind() == keyring.RecordOAuthClient && op.clientErr != nil {
		return nil, keyring.CutoverResult{}, op.clientErr
	}
	return op.refreshOperationFake.ReadActive(ctx, ns)
}
func (op *diagnosticRefreshOperation) InvalidateFencedExact(ctx context.Context, ns keyring.Namespace, callback keyring.AuthorityCallback) (keyring.CutoverResult, error) {
	result, _ := op.refreshOperationFake.InvalidateFencedExact(ctx, ns, callback)
	return result, op.invalidationErr
}

func TestRefreshSecondaryEffectsAndReadFailureReachDefaultSink(t *testing.T) {
	for _, mode := range []string{"client-read", "cancel-invalidation", "installed-audit"} {
		t.Run(mode, func(t *testing.T) {
			operation := &diagnosticRefreshOperation{refreshOperationFake: refreshOperationFake{token: mustTokenBytes(t, refreshGeneration(contract.TokenEndpointAuthClientSecretBasic)), client: []byte("client-secret")}}
			requester := &refreshRequesterFake{status: http.StatusOK, header: http.Header{"Content-Type": {contract.MediaTypeJSON}}, body: []byte(`{"access_token":"new-access","token_type":"Bearer","refresh_token":"new-refresh"}`)}
			store := refreshStoreFake{prepared: refreshPrepared(contract.TokenEndpointAuthClientSecretBasic)}
			switch mode {
			case "client-read":
				operation.clientErr = errors.New("credential database read: disk I/O error old-access old-refresh")
			case "cancel-invalidation":
				requester.err = context.Canceled
				requester.handoff = true
				operation.invalidationErr = errors.New("authority fence: audit denied old-access")
			case "installed-audit":
				store.auditHook = func(event contract.AuditEvent) error {
					if event.Phase == "outcome" {
						return errors.New("audit outcome: disk full new-access new-refresh client-secret")
					}
					return nil
				}
			}
			service := newRefreshService(store, &refreshCoordinatorFake{operation: operation}, refreshResolverFake{graph: refreshGraph()}, requester, refreshServerID, func() time.Time { return refreshNow }, nil, nil)
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
			service.SetDiagnostics(adapter, nil)
			_, err := service.Refresh(t.Context(), RefreshRequest{ServerID: refreshServerID})
			require.Error(t, err)
			service.Shutdown()
			require.True(t, adapter.Finish(nil))
			require.Equal(t, 1, bytes.Count(output.Bytes(), []byte(`"event":"oauth_refresh_failed"`)))
			switch mode {
			case "client-read":
				require.NotErrorIs(t, err, servers.ErrStaleRevision)
				require.Zero(t, requester.calls)
				require.Contains(t, output.String(), "read oauth_client generation")
				require.Contains(t, output.String(), "disk I/O error")
			case "cancel-invalidation":
				require.Equal(t, 1, requester.calls)
				require.Equal(t, 1, operation.invalidations)
				require.Contains(t, output.String(), "context canceled")
				require.Contains(t, output.String(), "authority invalidation unacknowledged")
				require.Contains(t, output.String(), "audit denied")
			case "installed-audit":
				require.Equal(t, 1, requester.calls)
				require.NotEmpty(t, operation.installed)
				require.Contains(t, output.String(), "token_installation=acknowledged revision=2")
				require.Contains(t, output.String(), "audit outcome: disk full")
			}
			for _, secret := range []string{"old-access", "old-refresh", "new-access", "new-refresh", "client-secret"} {
				require.NotContains(t, output.String(), secret)
			}
		})
	}
}
