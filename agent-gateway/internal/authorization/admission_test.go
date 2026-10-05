package authorization

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmissionRequestLocalConfirmationFences(t *testing.T) {
	for _, change := range []string{"unchanged", "original cancellation", "fresh cancellation", "revoke", "replace", "principal", "revision", "drain", "control latch", "release"} {
		t.Run(change, func(t *testing.T) {
			armed := false
			r, store := newRepository(t, func(point storage.FaultPoint) error {
				if armed && point == storage.FaultAfterCommit {
					return errors.New("uncertain control commit")
				}
				return nil
			})
			principal, credential := createAdmissionCredential(t, r)
			lease := mustAuthenticateLease(t, r, credential.Bearer)
			defer lease.Release()
			original, cancelOriginal := context.WithCancel(t.Context())
			defer cancelOriginal()
			evaluation, err := r.EvaluateAdmission(original, lease, "", ptrVerification(defaultResolvedVerification()))
			require.NoError(t, err)
			require.NotNil(t, evaluation.Candidate)
			require.Equal(t, leasePending, leasePhase(lease.phase.Load()))
			fresh, cancelFresh := context.WithCancel(t.Context())
			defer cancelFresh()
			switch change {
			case "original cancellation":
				cancelOriginal()
			case "fresh cancellation":
				cancelFresh()
			case "revoke":
				_, err = r.RevokeCredential(t.Context(), principal.ID, credential.Principal.Revision)
			case "replace":
				_, err = r.IssueCredential(t.Context(), principal.ID, credential.Principal.Revision)
			case "principal":
				name := "changed"
				_, err = r.PatchPrincipal(t.Context(), principal.ID, PatchPrincipalRequest{ExpectedRevision: credential.Principal.Revision, DisplayName: &name})
			case "revision":
				_, err = r.CreatePrincipal(t.Context(), CreatePrincipalRequest{DisplayName: "other", Visibility: contract.VisibilityAll})
			case "drain":
				r.BeginDrain()
			case "release":
				lease.Release()
			case "control latch":
				armed = true
				require.Error(t, store.Mutate(t.Context(), func(*sql.Tx) error { return nil }))
				require.True(t, store.Latched())
			}
			require.NoError(t, err)
			subject, err := r.ConfirmEvaluation(fresh, evaluation.Candidate, "")
			if change == "unchanged" {
				require.NoError(t, err)
				require.True(t, r.OwnsAdmittedSubject(subject))
				require.Equal(t, leaseAdmitted, leasePhase(lease.phase.Load()))
				// Revocation after admission never cancels or reauthorizes this execution.
				_, err = r.RevokeCredential(t.Context(), principal.ID, credential.Principal.Revision)
				require.NoError(t, err)
				assertLeaseOpen(t, lease)
			} else {
				require.Error(t, err)
				require.False(t, r.OwnsAdmittedSubject(subject))
				require.NotEqual(t, leaseAdmitted, leasePhase(lease.phase.Load()))
			}
			_, err = r.ConfirmEvaluation(t.Context(), evaluation.Candidate, "")
			require.Error(t, err, "every confirmation consumes the candidate")
		})
	}
}

func TestAdmissionCapturedExpiryAndAuthorityClock(t *testing.T) {
	r, _ := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, r)
	expiry := testNow.Add(time.Second)
	mustCreateEvaluationGrant(t, r, CreateGrantRequest{PrincipalID: principal.ID, Effect: contract.GrantAllow, ExpiresAt: &expiry, Target: accesstarget.MCP{ServerID: id(51)}})
	request := ResolvedVerification{Target: accesstarget.Tool(id(51), "tool"), Arguments: mustAdmissionArguments(t, `{}`)}
	lease := mustAuthenticateLease(t, r, credential.Bearer)
	defer lease.Release()
	evaluation, err := r.EvaluateAdmission(t.Context(), lease, "", &request)
	require.NoError(t, err)
	require.Equal(t, contract.DecisionAllow, evaluation.Result.Decision)
	r.clock.(*fixedClock).now = expiry
	_, err = r.ConfirmEvaluation(t.Context(), evaluation.Candidate, "")
	require.NoError(t, err, "confirmation keeps captured evaluation time")
	next := mustAuthenticateLease(t, r, credential.Bearer)
	defer next.Release()
	expired, err := r.EvaluateAdmission(t.Context(), next, "", &request)
	require.NoError(t, err)
	require.Equal(t, contract.DecisionBlock, expired.Result.Decision)
	require.Nil(t, expired.Candidate)
	r.clock.(*fixedClock).now = time.Time{}
	invalid, err := r.EvaluateAdmission(t.Context(), next, "", &request)
	require.Error(t, err)
	require.Nil(t, invalid.Candidate)
	_, err = r.EvaluateAdmission(t.Context(), next, "", nil)
	require.Error(t, err, "binding-only evaluation also needs valid authority time")
}

func TestAdmissionGateScopedVerificationIsOneUse(t *testing.T) {
	r, store := newRepository(t, nil)
	_, credential := createAdmissionCredential(t, r)
	lease := mustAuthenticateLease(t, r, credential.Bearer)
	defer lease.Release()
	var retained *Admission
	require.NoError(t, r.WithAdmission(t.Context(), lease, func(a *Admission) error {
		retained = a
		return store.View(t.Context(), func(tx *sql.Tx) error {
			_, phase, err := a.VerifyResolvedTx(t.Context(), tx, defaultResolvedVerification())
			require.NoError(t, err)
			require.Equal(t, ResolvedEvaluated, phase)
			_, _, err = a.VerifyResolvedTx(t.Context(), tx, defaultResolvedVerification())
			require.ErrorIs(t, err, ErrAdmissionUnavailable)
			return nil
		})
	}))
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
		_, _, err := retained.VerifyResolvedTx(t.Context(), tx, defaultResolvedVerification())
		require.ErrorIs(t, err, ErrAdmissionUnavailable)
		return nil
	}))
	require.Equal(t, leasePending, leasePhase(lease.phase.Load()), "evaluation alone never admits")
}

func TestAdmissionAddsNoInvocationPersistenceOrCapabilityUse(t *testing.T) {
	for _, path := range []string{"admission.go", "confirmation.go"} {
		source, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, forbidden := range []string{"CREATE TABLE", "INSERT INTO invocation", "internal/catalog", "internal/downstream", "AcquireCapability", "TrafficReceipt", "CommitSucceeded"} {
			assert.NotContains(t, string(source), forbidden)
		}
	}
}

func ptrVerification(value ResolvedVerification) *ResolvedVerification { return &value }
func createAdmissionCredential(t *testing.T, repository *Repository) (contract.Principal, contract.AgentCredentialCreation) {
	t.Helper()
	principal := mustCreatePrincipal(t, repository)
	credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
	require.NoError(t, err)
	return principal, credential
}
func defaultResolvedVerification() ResolvedVerification {
	return ResolvedVerification{Target: accesstarget.Tool(contract.SyntheticServerID, "tool"), Arguments: strictjson.Value{Type: strictjson.ValueObject}}
}
func mustAdmissionArguments(t *testing.T, value string) strictjson.Value {
	t.Helper()
	parsed, err := strictjson.ParseValue([]byte(value), strictjson.Options{MaxBytes: mustLimit("mcp_body_bytes"), MaxDepth: int(mustLimit("json_depth"))})
	require.NoError(t, err)
	return parsed
}
