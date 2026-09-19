package invocation

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficLocalUnknownSettlesPinWithoutTerminalClaim(t *testing.T) {
	traffic, _ := trafficFixture(t, nil, nil)
	receipt, err := traffic.Admit(t.Context(), trafficPrepared(1))
	require.NoError(t, err)
	require.True(t, traffic.Confirm(t.Context(), receipt))
	service := &Service{audits: &Repository{traffic: traffic}}
	response := service.finish(t.Context(), invocationID(1), receipt, CallOutcome{ErrorCode: contract.ToolUnavailable})
	assert.Equal(t, contract.ToolUnavailable, response.ErrorCode)
	assert.Equal(t, invocationID(1), response.InvocationID)
	history, err := traffic.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	assert.Nil(t, history.Records[0].CompletedAt)
	traffic.mu.Lock()
	assert.Empty(t, traffic.pins)
	traffic.mu.Unlock()
}

func TestTrafficAcknowledgedRefusalsReleasePins(t *testing.T) {
	for _, branch := range []string{"invalid_params", "unknown_tool", "invalid_arguments", "deny", "block"} {
		t.Run(branch, func(t *testing.T) {
			coordinator, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			traffic, _ := trafficFixture(t, nil, nil)
			audits.traffic = traffic
			class := contract.InvocationAdmissionClass(branch)
			if branch == "deny" || branch == "block" {
				class = contract.AdmissionEvaluated
				require.NoError(t, audits.store.Mutate(t.Context(), func(tx *sql.Tx) error {
					statement := `UPDATE grants SET effect='deny'`
					if branch == "block" {
						statement = `DELETE FROM grants`
					}
					_, err := tx.ExecContext(t.Context(), statement)
					return err
				}))
			}
			lease, err := authority.Authenticate(t.Context(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			result, err := coordinator.Admit(t.Context(), lease, identity, testAuditRequest(class))
			require.NoError(t, err)
			assert.True(t, result.Committed)
			assert.False(t, result.DispatchAuthorized)
			assert.Nil(t, result.Subject)
			if branch == "deny" || branch == "block" {
				require.NotNil(t, result.Decision)
				assert.Equal(t, branch, string(*result.Decision))
			}
			history, err := traffic.History(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			assert.Nil(t, history.Records[0].CompletedAt)
			traffic.mu.Lock()
			assert.Empty(t, traffic.pins)
			traffic.mu.Unlock()
		})
	}
}

func TestTrafficAdmissionConfirmationInterleavings(t *testing.T) {
	for _, race := range []string{"allow", "expiry", "revoke", "replace", "principal", "unrelated revision", "control fault", "cancel", "drain", "lost acknowledgment"} {
		t.Run(race, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var controlFault atomic.Bool
			coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, func(point storage.FaultPoint) error {
				if controlFault.Load() && point == storage.FaultAfterCommit {
					return errors.New("control commit uncertainty")
				}
				return nil
			})
			if race == "expiry" {
				require.NoError(t, audits.store.Mutate(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `UPDATE grants SET expires_at=?`, canonicalInvocationTime(invocationTestTime.Add(time.Second)))
					return err
				}))
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once, barrierOnce sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			traffic, _ := trafficFixture(t, nil, func(point string) error {
				if point == "before_begin" {
					barrierOnce.Do(func() {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
						}
					})
				}
				if point == "acknowledgment" && race == "lost acknowledgment" {
					return errors.New("lost acknowledgment")
				}
				return nil
			})
			audits.traffic = traffic
			lease, err := authority.Authenticate(ctx, credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			requestCtx, requestCancel := context.WithCancel(ctx)
			defer requestCancel()
			type outcome struct {
				result AdmissionResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				r, e := coordinator.Admit(requestCtx, lease, identity, testAuditRequest(contract.AdmissionEvaluated))
				done <- outcome{r, e}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("traffic persistence not entered")
			}
			// These complete while the actual traffic writer is held. No gate or
			// control write slot may span traffic fsync/commit settlement.
			switch race {
			case "expiry":
				audits.clock.(*repositoryClock).Set(invocationTestTime.Add(2 * time.Second))
			case "control fault":
				controlFault.Store(true)
				_, faultErr := authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "fault", Visibility: contract.VisibilityAll})
				require.Error(t, faultErr)
				require.True(t, audits.store.Latched())
			case "revoke":
				_, err = authority.RevokeCredential(ctx, principal.ID, credential.Principal.Revision)
			case "replace":
				_, err = authority.IssueCredential(ctx, principal.ID, credential.Principal.Revision)
			case "principal":
				name := "changed"
				_, err = authority.PatchPrincipal(ctx, principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, DisplayName: &name})
			case "unrelated revision":
				_, err = authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "unrelated", Visibility: contract.VisibilityAll})
			case "cancel":
				requestCancel()
			case "drain":
				authority.BeginDrain()
			}
			require.NoError(t, err)
			unblock()
			var got outcome
			select {
			case got = <-done:
			case <-ctx.Done():
				t.Fatal("admission did not settle")
			}
			if race == "allow" || race == "expiry" {
				require.NoError(t, got.err)
				require.True(t, got.result.DispatchAuthorized)
				require.NotNil(t, got.result.Subject)
				require.NoError(t, traffic.Complete(ctx, got.result.receipt, trafficCompletion()))
			} else {
				require.Error(t, got.err)
				assert.False(t, got.result.DispatchAuthorized)
				assert.Nil(t, got.result.Subject)
			}
			history, err := traffic.History(ctx, 0, 10)
			require.NoError(t, err)
			if race != "cancel" {
				require.Len(t, history.Records, 1)
			}
			if race != "allow" && race != "expiry" && len(history.Records) != 0 {
				assert.Nil(t, history.Records[0].CompletedAt)
			}
			traffic.mu.Lock()
			assert.Empty(t, traffic.pins)
			traffic.mu.Unlock()
		})
	}
}
