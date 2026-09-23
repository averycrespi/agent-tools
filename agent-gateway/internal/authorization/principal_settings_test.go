package authorization

import (
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestPrincipalSettingsUnifiedRevisionAndPolicy(t *testing.T) {
	r, store := newRepository(t, nil)
	p, credential := createAdmissionCredential(t, r)
	ctx := t.Context()
	p, err := r.GetPrincipal(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, contract.HTTPDefaultBlock, p.HTTPDefault)
	input := HTTPAccessInput{PrincipalID: p.ID, URL: "https://example.com/", Method: "GET"}
	before, err := r.PreviewHTTPAccess(ctx, input)
	require.NoError(t, err)
	require.False(t, before.Decision.Allowed)
	allow, block := contract.HTTPDefaultAllow, contract.HTTPDefaultBlock
	first, err := r.PatchPrincipal(ctx, p.ID, PatchPrincipalRequest{ExpectedRevision: p.Revision, HTTPDefault: &allow})
	require.NoError(t, err)
	require.NotEqual(t, p.Revision, first.Revision)
	require.Equal(t, p.Credential, first.Credential)
	_, err = r.PatchPrincipal(ctx, p.ID, PatchPrincipalRequest{ExpectedRevision: p.Revision, DisplayName: stringPointer("stale identity")})
	require.ErrorIs(t, err, ErrStaleRevision)
	after, err := r.PreviewHTTPAccess(ctx, input)
	require.NoError(t, err)
	require.True(t, after.Decision.Allowed)
	require.Equal(t, before.Decision.DefaultRevision+1, after.Decision.DefaultRevision)
	require.Equal(t, before.Decision.PolicyRevision+1, after.Decision.PolicyRevision)
	second, err := r.PatchPrincipal(ctx, p.ID, PatchPrincipalRequest{ExpectedRevision: first.Revision, DisplayName: stringPointer("Renamed")})
	require.NoError(t, err)
	require.Equal(t, allow, second.HTTPDefault)
	_, err = r.PatchPrincipal(ctx, p.ID, PatchPrincipalRequest{ExpectedRevision: first.Revision, HTTPDefault: &block})
	require.ErrorIs(t, err, ErrStaleRevision)
	disabled, visibility := contract.PrincipalDisabled, contract.VisibilityAll
	mixed, err := r.PatchPrincipal(ctx, p.ID, PatchPrincipalRequest{ExpectedRevision: second.Revision, DisplayName: stringPointer("Combined"), State: &disabled, Visibility: &visibility, HTTPDefault: &block})
	require.NoError(t, err)
	require.Equal(t, "Combined", mixed.DisplayName)
	require.Equal(t, disabled, mixed.State)
	require.Equal(t, visibility, mixed.Visibility)
	require.Equal(t, block, mixed.HTTPDefault)
	require.Nil(t, mixed.Credential)
	_, err = r.Authenticate(ctx, credential.Bearer)
	require.Error(t, err)
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		var revision int
		err := tx.QueryRowContext(ctx, `SELECT revision FROM http_defaults WHERE principal_id=?`, p.ID).Scan(&revision)
		require.Equal(t, 3, revision)
		return err
	}))
}

func TestPrincipalSettingsRollbackAllFieldsAndEvidence(t *testing.T) {
	for _, fault := range []struct{ name, sql string }{
		{"principal", `CREATE TRIGGER fail_settings BEFORE UPDATE ON principals BEGIN SELECT RAISE(ABORT, 'injected'); END`},
		{"default", `CREATE TRIGGER fail_settings BEFORE UPDATE ON http_defaults BEGIN SELECT RAISE(ABORT, 'injected'); END`},
		{"revision", `CREATE TRIGGER fail_settings BEFORE UPDATE ON authorization_meta BEGIN SELECT RAISE(ABORT, 'injected'); END`},
		{"audit", `CREATE TRIGGER fail_settings BEFORE INSERT ON control_audit_events BEGIN SELECT RAISE(ABORT, 'injected'); END`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			r, store := newRepository(t, nil)
			p, _ := createAdmissionCredential(t, r)
			ctx := t.Context()
			before, err := r.GetPrincipal(ctx, p.ID)
			require.NoError(t, err)
			revision := authorizationRevision(t, r)
			snapshot := func() [2]int {
				var value [2]int
				require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
					return tx.QueryRowContext(ctx, `SELECT (SELECT revision FROM http_defaults WHERE principal_id=?), (SELECT count(*) FROM control_audit_events)`, p.ID).Scan(&value[0], &value[1])
				}))
				return value
			}
			evidence := snapshot()
			require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, fault.sql); return err }))
			allow, disabled, visibility := contract.HTTPDefaultAllow, contract.PrincipalDisabled, contract.VisibilityAll
			_, err = r.PatchPrincipal(ctx, p.ID, PatchPrincipalRequest{ExpectedRevision: before.Revision, DisplayName: stringPointer("Must roll back"), State: &disabled, Visibility: &visibility, HTTPDefault: &allow})
			require.ErrorIs(t, err, ErrStorageUnavailable)
			after, err := r.GetPrincipal(ctx, p.ID)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, revision, authorizationRevision(t, r))
			require.Equal(t, evidence, snapshot())
		})
	}
}

func TestPrincipalSettingsRejectInvalidAndNoop(t *testing.T) {
	r, _ := newRepository(t, nil)
	created, err := r.CreatePrincipal(t.Context(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	p := created.Principal
	for _, value := range []contract.HTTPDefault{"", "ALLOW", "unknown"} {
		_, err := r.PatchPrincipal(t.Context(), p.ID, PatchPrincipalRequest{ExpectedRevision: p.Revision, DisplayName: stringPointer("Must not save"), HTTPDefault: &value})
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	block := contract.HTTPDefaultBlock
	_, err = r.PatchPrincipal(t.Context(), p.ID, PatchPrincipalRequest{ExpectedRevision: p.Revision, HTTPDefault: &block})
	require.ErrorIs(t, err, ErrConflict)
	after, err := r.GetPrincipal(t.Context(), p.ID)
	require.NoError(t, err)
	require.Equal(t, p, after)
	page, err := r.ListPrincipals(t.Context(), nil, 10)
	require.NoError(t, err)
	require.Equal(t, []contract.Principal{p}, page.Items)
}
