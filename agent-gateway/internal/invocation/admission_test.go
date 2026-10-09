package invocation

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
	"github.com/stretchr/testify/require"
)

func TestAdmissionBindingOnlyAndPolicyBranches(t *testing.T) {
	for _, branch := range []string{"invalid_params", "unknown_tool", "invalid_arguments", "allow", "deny", "block", "unavailable"} {
		t.Run(branch, func(t *testing.T) {
			c, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
			class := contract.InvocationAdmissionClass(branch)
			if branch == "allow" || branch == "deny" || branch == "block" || branch == "unavailable" {
				class = contract.AdmissionEvaluated
			}
			if branch == "deny" {
				_, err := authority.CreateGrant(t.Context(), authorization.CreateGrantRequest{PrincipalID: principal.ID, Effect: contract.GrantDeny, Target: accesstarget.MCP{ServerID: contract.SyntheticServerID}}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
				require.NoError(t, err)
			}
			if branch == "unavailable" {
				insertMalformedEvaluationGrant(t, audits.store, principal.ID)
			}
			lease, err := authority.Authenticate(t.Context(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			request := testAuditRequest(class)
			if branch == "block" {
				request.MCP.Route.Target.ServerID = invocationID(500)
			}
			result, err := c.Admit(t.Context(), lease, identity, request)
			require.NoError(t, err)
			require.True(t, result.Evaluated)
			require.Equal(t, branch == "allow", result.DispatchAuthorized)
			if result.DispatchAuthorized {
				require.NotNil(t, result.Subject)
				require.True(t, authority.OwnsAdmittedSubject(*result.Subject))
			} else {
				require.Nil(t, result.Subject)
			}
			waitTraffic(t, audits.traffic)
			record, found, err := audits.Read(t.Context(), identity.InvocationID)
			require.NoError(t, err)
			require.True(t, found)
			switch {
			case branch == "unavailable":
				require.Equal(t, contract.AdmissionAuthorizationUnavailable, record.AdmissionClass)
				require.Nil(t, record.AuthorizationDecision)
			case class == contract.AdmissionEvaluated:
				require.NotNil(t, record.AuthorizationDecision)
				require.Equal(t, branch, string(*record.AuthorizationDecision))
			default:
				require.Nil(t, record.AuthorizationDecision)
			}
		})
	}
}

func TestAdmissionCaptureFailureDoesNotChangeAuthority(t *testing.T) {
	for _, capture := range []string{"identity", "redaction", "unavailable"} {
		t.Run(capture, func(t *testing.T) {
			c, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			lease, err := authority.Authenticate(t.Context(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			identity, err := audits.PrepareIdentity()
			require.NoError(t, err)
			request := testAuditRequest(contract.AdmissionEvaluated)
			switch capture {
			case "identity":
				identity = PreparedAdmission{}
			case "redaction":
				request.MCP.RedactedArguments = nil
			case "unavailable":
				audits.traffic.BeginDrain()
			}
			result, err := c.Admit(t.Context(), lease, identity, request)
			require.NoError(t, err)
			require.True(t, result.DispatchAuthorized)
			require.NotNil(t, result.Subject)
			require.True(t, authority.OwnsAdmittedSubject(*result.Subject))
			waitTraffic(t, audits.traffic)
			count, err := audits.Count(t.Context())
			require.NoError(t, err)
			require.Zero(t, count)
			// Reusing the same pending lease cannot execute a second time.
			repeated, err := c.Admit(t.Context(), lease, identity, request)
			require.Error(t, err)
			require.False(t, repeated.DispatchAuthorized)
		})
	}
}

func TestAdmissionHistoryCannotMaskRevocationOrControlFault(t *testing.T) {
	c, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	identity, err := audits.PrepareIdentity()
	require.NoError(t, err)
	audits.traffic.BeginDrain()
	_, err = authority.RevokeCredential(t.Context(), principal.ID, credential.Principal.Revision)
	require.NoError(t, err)
	result, err := c.Admit(t.Context(), lease, identity, testAuditRequest(contract.AdmissionEvaluated))
	require.Error(t, err)
	require.False(t, result.DispatchAuthorized)
	require.Nil(t, result.Subject)
}

func TestAdmissionImplementationHasNoHistoryPermissionOrMutation(t *testing.T) {
	source, err := os.ReadFile("admission.go")
	require.NoError(t, err)
	for _, forbidden := range []string{".mutate(", ".InsertTx(", "TrafficReceipt", "confirmReceipt", ".admissionGate"} {
		require.NotContains(t, string(source), forbidden)
	}
	require.Contains(t, string(source), "ConfirmEvaluation")
}

func newAdmissionCoordinator(t *testing.T, fault func(storage.FaultPoint) error) (*AdmissionCoordinator, *Repository, *authorization.Repository, contract.Principal, contract.AgentCredentialCreation) {
	t.Helper()
	coordinator, audits, authority, principal, credential, _ := newAdmissionCoordinatorWithOwnership(t, fault)
	return coordinator, audits, authority, principal, credential
}

func newAdmissionCoordinatorWithOwnership(t *testing.T, fault func(storage.FaultPoint) error) (*AdmissionCoordinator, *Repository, *authorization.Repository, contract.Principal, contract.AgentCredentialCreation, *gatewaypaths.Ownership) {
	t.Helper()
	audits, store, clock, owner := newInvocationRepositoryWithOwnership(t, fault, entropyBytes(1024))
	audits.traffic, _ = trafficFixture(t, nil, nil)
	authority, err := authorization.New(store, clock, entropyBytes(8192))
	require.NoError(t, err)
	principal, err := authority.CreatePrincipal(t.Context(), authorization.CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	credential, err := authority.IssueCredential(t.Context(), principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	coordinator, err := NewAdmissionCoordinator(audits, authority)
	require.NoError(t, err)
	return coordinator, audits, authority, principal.Principal, credential, owner
}
func testAuditRequest(class contract.InvocationAdmissionClass) AuditAdmissionRequest {
	name := "namespace.tool"
	request := AuditAdmissionRequest{Class: class, MCP: MCPDetails{RequestedName: &name, RedactedArguments: []byte(`{}`)}}
	if class == contract.AdmissionInvalidParams {
		request.MCP.RequestedName = nil
		request.MCP.RedactedArguments = nil
	}
	if class == contract.AdmissionInvalidArguments || class == contract.AdmissionEvaluated {
		route := testRoute()
		route.Target.ServerID = contract.SyntheticServerID
		request.MCP.Route = &route
	}
	if class == contract.AdmissionEvaluated {
		request.Arguments = strictjson.Value{Type: strictjson.ValueObject}
	}
	return request
}
func insertMalformedEvaluationGrant(t *testing.T, store *storage.Store, principalID string) {
	t.Helper()
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(t.Context(), `PRAGMA ignore_check_constraints=ON`); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), `INSERT INTO grants (id,principal_id,effect,server_id,upstream_name,constraint_json,expires_at,created_at) VALUES (?,?,'bogus',?,'tool',NULL,NULL,?)`, invocationID(900), principalID, contract.SyntheticServerID, canonicalInvocationTime(invocationTestTime))
		return err
	}))
}
