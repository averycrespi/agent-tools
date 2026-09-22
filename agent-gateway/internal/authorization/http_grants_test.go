package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

const allowHTTPPolicy = `{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}}}`

func TestHTTPDefaultsGrantsPreviewAndExpiry(t *testing.T) {
	r, store := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, r)
	ctx := t.Context()
	def, err := r.GetHTTPDefault(ctx, principal.ID)
	require.NoError(t, err)
	require.Equal(t, contract.HTTPDefaultBlock, def.Default)
	before, err := r.GetPrincipal(ctx, principal.ID)
	require.NoError(t, err)
	input := HTTPAccessInput{PrincipalID: principal.ID, URL: "https://example.com/private?secret=not-retained", Method: "GET"}
	preview, err := r.PreviewHTTPAccess(ctx, input)
	require.NoError(t, err)
	require.False(t, preview.Decision.Allowed)
	require.True(t, preview.PolicyOnly)
	require.False(t, preview.NetworkVerified || preview.TLSVerified || preview.MaterialVerified || preview.AdmissionAuthority)
	expiry := testNow.Add(time.Hour)
	grant, err := r.PutHTTPGrant(ctx, "", "", HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(allowHTTPPolicy), ExpiresAt: &expiry})
	require.NoError(t, err)
	require.Equal(t, "1", grant.Revision)
	require.Equal(t, contract.GrantActive, grant.State)
	preview, err = r.PreviewHTTPAccess(ctx, input)
	require.NoError(t, err)
	require.True(t, preview.Decision.Allowed)
	lease := mustAuthenticateLease(t, r, credential.Bearer)
	defer lease.Release()
	decision, err := r.EvaluateHTTPAccess(ctx, lease, input, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}})
	require.NoError(t, err)
	require.Equal(t, preview.Decision, decision)
	// Policy edits invalidate pending confirmation through the existing authority.
	pending := mustAuthenticateLease(t, r, credential.Bearer)
	defer pending.Release()
	evaluation, err := r.EvaluateAdmission(ctx, pending, id(88), &ResolvedVerification{Target: accesstarget.Tool(contract.SyntheticServerID, "tool"), Arguments: mustAdmissionArguments(t, `{}`)})
	require.NoError(t, err)
	require.NotNil(t, evaluation.Candidate)
	def, err = r.SetHTTPDefault(ctx, principal.ID, def.Revision, contract.HTTPDefaultAllow)
	require.NoError(t, err)
	_, err = r.ConfirmEvaluation(ctx, evaluation.Candidate, id(88), func(_ contract.AuthorizationResult, detach func() bool) bool { return detach() })
	require.ErrorIs(t, err, ErrAuthorizationUnavailable)
	after, err := r.GetPrincipal(ctx, principal.ID)
	require.NoError(t, err)
	require.Equal(t, before, after, "HTTP defaults must not reinterpret Principal compatibility")
	_, err = r.SetHTTPDefault(ctx, principal.ID, "1", contract.HTTPDefaultBlock)
	require.ErrorIs(t, err, ErrStaleRevision)
	_, err = r.SetHTTPDefault(ctx, principal.ID, def.Revision, contract.HTTPDefaultBlock)
	require.NoError(t, err)
	r.clock.(*fixedClock).now = expiry
	preview, err = r.PreviewHTTPAccess(ctx, input)
	require.NoError(t, err)
	require.False(t, preview.Decision.Allowed)
	expired, err := r.GetHTTPGrant(ctx, grant.ID)
	require.NoError(t, err)
	require.Equal(t, contract.GrantExpired, expired.State)
	require.NoError(t, r.DeleteHTTPGrant(ctx, grant.ID, grant.Revision))
	// Request coordinates must not be persisted by preview.
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		var n int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM control_audit_events WHERE event LIKE '%not-retained%'`).Scan(&n)
		if err == nil {
			require.Zero(t, n)
		}
		return err
	}))
}

func TestHTTPGrantAtomicReplacementAndCursor(t *testing.T) {
	r, _ := newRepository(t, nil)
	principal, _ := createAdmissionCredential(t, r)
	ctx := context.Background()
	input := HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(allowHTTPPolicy)}
	first, err := r.PutHTTPGrant(ctx, "", "", input)
	require.NoError(t, err)
	second, err := r.PutHTTPGrant(ctx, "", "", input)
	require.NoError(t, err)
	page, err := r.QueryHTTPGrants(ctx, CollectionQuery{PrincipalID: principal.ID}, nil, 1)
	require.NoError(t, err)
	require.Equal(t, 2, page.TotalCount)
	require.NotNil(t, page.Next)
	input.Policy = json.RawMessage(`{"version":1,"type":"block_destination","destination":{"host":"EXAMPLE.com","port":443}}`)
	edited, err := r.PutHTTPGrant(ctx, first.ID, first.Revision, input)
	require.NoError(t, err)
	require.Equal(t, first.ID, edited.ID)
	require.Equal(t, "2", edited.Revision)
	require.Contains(t, string(edited.Policy), `"host":"example.com"`)
	_, err = r.PutHTTPGrant(ctx, first.ID, first.Revision, input)
	require.ErrorIs(t, err, ErrStaleRevision)
	require.ErrorIs(t, r.DeleteHTTPGrant(ctx, first.ID, first.Revision), ErrStaleRevision)
	_, err = r.QueryHTTPGrants(ctx, CollectionQuery{PrincipalID: principal.ID}, page.Next, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	preview, err := r.PreviewHTTPAccess(ctx, HTTPAccessInput{PrincipalID: principal.ID, URL: "https://example.com/", Method: "GET"})
	require.NoError(t, err)
	require.Equal(t, contract.HTTPReasonDestinationBlock, preview.Decision.Reason)
	require.NoError(t, r.DeleteHTTPGrant(ctx, second.ID, second.Revision))
}
