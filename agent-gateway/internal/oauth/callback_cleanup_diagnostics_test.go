package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

type cleanupCallbackStore struct {
	*callbackFlowStore
	handle  keyring.Handle
	message string
}

func (store *cleanupCallbackStore) OAuthTokenAuthorityCallback(servers.OAuthTokenFence) (keyring.AuthorityCallback, error) {
	return func(ctx context.Context, tx *sql.Tx, update keyring.AuthorityUpdate) (string, error) {
		if !update.ActivateOnly {
			return "1", nil
		}
		// Seed retained bookkeeping only at activation: pre-install cleanup must
		// stay healthy so the real post-success cleanup owns this injected fault.
		if _, err := tx.ExecContext(ctx, `INSERT INTO keyring_candidates(owner,kind,handle,created_at) VALUES(?,?,?,?)`, update.Owner, string(update.Kind), string(store.handle), flowTime); err != nil {
			return "", err
		}
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS refuse_retained_cleanup BEFORE DELETE ON keyring_candidates WHEN OLD.handle = '`+string(store.handle)+`' BEGIN SELECT RAISE(FAIL, '`+store.message+`'); END`)
		return "1", err
	}, nil
}

type callbackCleanupObserver struct {
	t       *testing.T
	service *FlowService
	adapter *diagnostics.Adapter
	calls   int
}

func (observer *callbackCleanupObserver) HTTPProxy(facts diagnostics.Facts) {
	require.Zero(observer.t, observer.service.CallbackStatus().InUse, "cleanup snapshot emitted inside callback admission")
	observer.calls++
	observer.adapter.HTTPProxy(facts)
}

func TestCallbackCleanupRetainsSuccessAndMasksGenerationValuesAfterAdmission(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		name := "short"
		if oversized {
			name = "truncation_boundary"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0700))
			owner, err := gatewaypaths.AcquireForMaintenance(root)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, owner.Close()) })
			const installation = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
			database, err := storage.Initialize(t.Context(), owner, installation)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			clock := testutil.NewFakeClock(flowTime)
			require.NoError(t, keyring.SetupCustody(t.Context(), owner, database, clock))
			provider, err := keyring.NewProvider(installation)
			require.NoError(t, err)
			require.NoError(t, provider.UseDatabaseCustody(t.Context(), owner, database))
			coordinator := keyring.NewCoordinator(provider, database, clock, rand.Reader)
			bundle := callbackBundle("owned-state", false, contract.TokenEndpointAuthNone)
			namespace, err := keyring.NewNamespace(installation, bundle.serverID, keyring.RecordOAuthTokens)
			require.NoError(t, err)
			prior, err := coordinator.Replace(t.Context(), namespace, []byte("old-generation"))
			require.NoError(t, err)
			access, refresh := "constituent-access-canary", "constituent-refresh-canary"
			if oversized {
				access += strings.Repeat("x", 700)
			}
			body, err := json.Marshal(map[string]string{"access_token": access, "refresh_token": refresh, "token_type": "Bearer"})
			require.NoError(t, err)
			requester := &tokenRequester{status: http.StatusOK, header: http.Header{"Content-Type": {"application/json"}}, body: body}
			store := &cleanupCallbackStore{callbackFlowStore: &callbackFlowStore{flowStoreFake: flowStoreFake{created: flowCreateResult(contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic})}}, handle: prior.Handle, message: "retained cleanup refused " + access + " " + refresh}
			service := newFlowService(store, flowResolverFake{}, &flowRegistrarFake{}, zeroReader{}, bundle.registration.CallbackURL, func() time.Time { return flowTime })
			service.configureCallback(requester, coordinator, installation)
			service.byState[bundle.state] = bundle
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
			observer := &callbackCleanupObserver{t: t, service: service, adapter: adapter}
			coordinator.SetDiagnostics(observer)
			service.SetDiagnostics(adapter, nil)
			result := service.HandleCallback(t.Context(), "state=owned-state&code=one-code")
			require.True(t, adapter.Finish(nil))
			require.Equal(t, CallbackSucceeded, result.Outcome, output.String())
			require.Len(t, requester.requests, 1)
			require.Equal(t, 1, observer.calls)
			loaded, current, err := coordinator.ReadActive(t.Context(), namespace)
			require.NoError(t, err)
			require.NotEqual(t, prior.Handle, current.Handle)
			require.Contains(t, string(loaded), access)
			require.Contains(t, string(loaded), refresh)
			clear(loaded)
			status, err := coordinator.CandidateStatus(t.Context())
			require.NoError(t, err)
			require.EqualValues(t, 1, status.InUse)
			service.Shutdown()
			require.True(t, adapter.Finish(nil))
			require.Contains(t, output.String(), `"event":"operator_failure"`)
			require.Contains(t, output.String(), "publication and activation acknowledged")
			require.Contains(t, output.String(), "cleanup=unconfirmed")
			if oversized {
				require.Contains(t, output.String(), "detail withheld: truncated before source masking")
			} else {
				require.Contains(t, output.String(), "retained cleanup refused")
				require.Contains(t, output.String(), "physical_delete_ack=1 bookkeeping_delete_ack=0")
			}
			for _, secret := range []string{"constituent-access-canary", refresh, "one-code", "owned-state"} {
				require.NotContains(t, output.String(), secret)
			}
		})
	}
}
