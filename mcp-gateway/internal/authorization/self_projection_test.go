package authorization

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelfProjectionReturnsOnlyAdmittedIdentityAndCurrentOrdinaryGrants(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, repository)
	subject := admitSelfProjectionSubject(t, repository, credential.Bearer)

	seedProjectedGrant(t, store, projectedGrantRow{
		id: id(11), principalID: principal.ID, effect: contract.GrantAllow, serverID: id(51),
	})
	seedProjectedGrant(t, store, projectedGrantRow{
		id: id(12), principalID: principal.ID, effect: contract.GrantDeny, serverID: id(51), upstreamName: "echo",
		constraint: `{"equals":{"/n":1.0}}`, expiresAt: timePointer(testNow),
	})
	seedProjectedGrant(t, store, projectedGrantRow{
		id: id(13), principalID: principal.ID, effect: contract.GrantAllow, serverID: id(52), upstreamName: "retired",
		expiresAt: timePointer(testNow.Add(time.Hour)),
	})
	seedProjectedGrant(t, store, projectedGrantRow{
		id: id(15), principalID: principal.ID, effect: contract.GrantDeny, serverID: id(51), upstreamName: "active-deny",
	})
	seedProjectedGrant(t, store, projectedGrantRow{
		id: id(16), principalID: principal.ID, effect: contract.GrantAllow, serverID: id(52), upstreamName: "expired-allow",
		expiresAt: timePointer(testNow),
	})
	other := mustCreatePrincipal(t, repository)
	seedProjectedGrant(t, store, projectedGrantRow{
		id: id(14), principalID: other.ID, effect: contract.GrantDeny, serverID: id(51),
	})

	targets := &fakeSelfGrantTargets{namespaces: map[string]string{
		contract.SyntheticServerID: contract.SyntheticServerNamespace,
		id(51):                     "sample",
		id(52):                     "deleted",
	}}
	service, err := NewSelfProjectionService(repository, targets)
	require.NoError(t, err)
	identity, err := service.ReadSelfIdentity(context.Background(), subject)
	require.NoError(t, err)
	assert.Equal(t, contract.SelfIdentity{
		ID: principal.ID, DisplayName: "Agent", State: contract.PrincipalActive,
		Visibility: contract.VisibilityRequestable, PrincipalRevision: credential.Principal.Revision,
		CredentialRevision: credential.Principal.CredentialRevision,
	}, identity)

	first, err := service.ListSelfGrants(context.Background(), subject, nil, 2)
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotNil(t, first.Next)
	assert.Equal(t, contract.SyntheticServerNamespace, first.Items[0].Policy.Target)
	assert.Equal(t, contract.PolicyServer, first.Items[0].Policy.Scope)
	assert.Equal(t, id(11), first.Items[1].ID)
	assert.Equal(t, "sample", first.Items[1].Policy.Target)
	assert.Equal(t, contract.PolicyServer, first.Items[1].Policy.Scope)

	second, err := service.ListSelfGrants(context.Background(), subject, first.Next, 10)
	require.NoError(t, err)
	require.Len(t, second.Items, 4)
	assert.Nil(t, second.Next)
	assert.Equal(t, id(12), second.Items[0].ID)
	assert.Equal(t, contract.GrantDeny, second.Items[0].Effect)
	assert.Equal(t, contract.GrantExpired, second.Items[0].State)
	assert.Equal(t, contract.PolicyTool, second.Items[0].Policy.Scope)
	assert.Equal(t, "sample.echo", second.Items[0].Policy.Target)
	require.NotNil(t, second.Items[0].Policy.Constraint)
	assert.Equal(t, `{"equals":{"/n":1.0}}`, string(*second.Items[0].Policy.Constraint))
	assert.Equal(t, id(13), second.Items[1].ID)
	assert.Equal(t, contract.GrantActive, second.Items[1].State)
	assert.Equal(t, "deleted.retired", second.Items[1].Policy.Target)
	assert.Equal(t, contract.GrantDeny, second.Items[2].Effect)
	assert.Equal(t, contract.GrantActive, second.Items[2].State)
	assert.Equal(t, contract.GrantAllow, second.Items[3].Effect)
	assert.Equal(t, contract.GrantExpired, second.Items[3].State)
	assert.Equal(t, []string{contract.SyntheticServerID, id(51), id(51), id(52)}, targets.lookups)
}

func TestSelfProjectionRejectsForeignStaleOrMalformedSubjectsAndTargets(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, repository)
	subject := admitSelfProjectionSubject(t, repository, credential.Bearer)
	service, err := NewSelfProjectionService(repository, &fakeSelfGrantTargets{namespaces: map[string]string{
		contract.SyntheticServerID: contract.SyntheticServerNamespace,
	}})
	require.NoError(t, err)

	otherRepository, _ := newRepository(t, nil)
	foreignService, err := NewSelfProjectionService(otherRepository, &fakeSelfGrantTargets{})
	require.NoError(t, err)
	_, err = foreignService.ReadSelfIdentity(context.Background(), subject)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)
	_, err = service.ReadSelfIdentity(context.Background(), AdmittedSubject{})
	assert.ErrorIs(t, err, ErrAuthenticationRequired)

	_, err = repository.IssueCredential(context.Background(), principal.ID, credential.Principal.Revision)
	require.NoError(t, err)
	_, err = service.ReadSelfIdentity(context.Background(), subject)
	assert.ErrorIs(t, err, ErrAuthenticationRequired)

	freshPrincipal, freshCredential := createAdmissionCredential(t, repository)
	freshSubject := admitSelfProjectionSubject(t, repository, freshCredential.Bearer)
	seedProjectedGrant(t, store, projectedGrantRow{
		id: id(21), principalID: freshPrincipal.ID, effect: contract.GrantAllow, serverID: id(59),
	})
	page, err := service.ListSelfGrants(context.Background(), freshSubject, nil, 100)
	assert.ErrorIs(t, err, ErrInvalidState)
	assert.Empty(t, page.Items)
	_, err = service.ListSelfGrants(context.Background(), freshSubject, &SelfGrantCursor{Upper: 1, After: 2, AfterID: id(21)}, 100)
	assert.ErrorIs(t, err, ErrStaleCursor)
	_, err = service.ListSelfGrants(context.Background(), freshSubject, nil, 101)
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestSelfProjectionTargetFailureReturnsNoPartialPage(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, repository)
	subject := admitSelfProjectionSubject(t, repository, credential.Bearer)
	seedProjectedGrant(t, store, projectedGrantRow{id: id(31), principalID: principal.ID, effect: contract.GrantAllow, serverID: id(51)})
	targets := &fakeSelfGrantTargets{
		namespaces: map[string]string{contract.SyntheticServerID: contract.SyntheticServerNamespace},
		err:        errors.New("target storage failed"),
	}
	service, err := NewSelfProjectionService(repository, targets)
	require.NoError(t, err)
	page, err := service.ListSelfGrants(context.Background(), subject, nil, 100)
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.Empty(t, page.Items)
}

func TestSelfProjectionSubjectExistsOnlyAfterAcknowledgedAllow(t *testing.T) {
	repository, store := newRepository(t, nil)
	_, credential := createAdmissionCredential(t, repository)
	lease, err := repository.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	var token *PendingDetachment
	require.NoError(t, repository.WithAdmission(context.Background(), lease, func(admission *Admission) error {
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			result, pending, phase, verifyErr := admission.VerifyResolvedTx(context.Background(), transaction, defaultResolvedVerification())
			require.NoError(t, verifyErr)
			assert.Equal(t, contract.DecisionAllow, result.Decision)
			assert.Equal(t, ResolvedEvaluated, phase)
			token = pending
			_, subjectErr := token.Subject()
			assert.ErrorIs(t, subjectErr, ErrAdmissionUnavailable)
			return nil
		}))
		require.NoError(t, token.CommitSucceeded())
		_, subjectErr := token.Subject()
		return subjectErr
	}))
}

func TestAdmittedSubjectContainsOnlySafeAdmissionIdentity(t *testing.T) {
	repository, _ := newRepository(t, nil)
	_, credential := createAdmissionCredential(t, repository)
	subject := admitSelfProjectionSubject(t, repository, credential.Bearer)
	assert.True(t, repository.OwnsAdmittedSubject(subject))
	assert.Equal(t, credential.Principal.ID, subject.PrincipalID())
	assert.Equal(t, credential.Principal.Revision, subject.PrincipalRevision())
	require.NotNil(t, credential.Principal.Credential)
	assert.Equal(t, credential.Principal.Credential.ID, subject.CredentialID())
	assert.Equal(t, credential.Principal.CredentialRevision, subject.CredentialRevision())
	assert.NotEmpty(t, subject.AuthorizationRevision())
	assert.NotContains(t, subject.PrincipalID()+subject.CredentialID(), credential.Bearer)
}

func TestSelfProjectionAdmittedSubjectContainsNoSecretSink(t *testing.T) {
	repository, _ := newRepository(t, nil)
	_, credential := createAdmissionCredential(t, repository)
	subject := admitSelfProjectionSubject(t, repository, credential.Bearer)
	assert.Equal(t, credential.Principal.ID, subject.PrincipalID())
	assert.Equal(t, credential.Principal.Revision, subject.PrincipalRevision())
	require.NotNil(t, credential.Principal.Credential)
	assert.Equal(t, credential.Principal.Credential.ID, subject.CredentialID())
	assert.Equal(t, credential.Principal.CredentialRevision, subject.CredentialRevision())
	assert.NotEmpty(t, subject.AuthorizationRevision())
}

func admitSelfProjectionSubject(t *testing.T, repository *Repository, bearer string) AdmittedSubject {
	t.Helper()
	lease, err := repository.Authenticate(context.Background(), bearer)
	require.NoError(t, err)
	_, token, err := verifyResolvedMutation(repository, repository.store, lease, defaultResolvedVerification(), nil)
	require.NoError(t, err)
	require.NotNil(t, token)
	subject, err := token.Subject()
	require.NoError(t, err)
	return subject
}

type projectedGrantRow struct {
	id, principalID, serverID string
	effect                    contract.GrantEffect
	upstreamName              string
	constraint                string
	expiresAt                 *time.Time
}

func seedProjectedGrant(t *testing.T, store interface {
	Mutate(context.Context, func(*sql.Tx) error) error
}, row projectedGrantRow) {
	t.Helper()
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		var upstream, constraint, expires any
		if row.upstreamName != "" {
			upstream = row.upstreamName
		}
		if row.constraint != "" {
			constraint = row.constraint
		}
		if row.expiresAt != nil {
			expires = timestamp(*row.expiresAt)
		}
		_, err := transaction.Exec(`INSERT INTO grants
			(id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, row.id, row.principalID, row.effect, row.serverID,
			upstream, constraint, expires, timestamp(testNow.Add(-time.Hour)))
		return err
	}))
}

type fakeSelfGrantTargets struct {
	namespaces map[string]string
	lookups    []string
	err        error
}

func (targets *fakeSelfGrantTargets) LookupStoredGrantNamespaceTx(_ context.Context, transaction *sql.Tx, serverID string) (string, bool, error) {
	if transaction == nil {
		return "", false, errors.New("missing supplied transaction")
	}
	if targets.err != nil && serverID != contract.SyntheticServerID {
		return "", false, targets.err
	}
	targets.lookups = append(targets.lookups, serverID)
	namespace, found := targets.namespaces[serverID]
	return namespace, found, nil
}
