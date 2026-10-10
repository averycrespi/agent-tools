package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func gitRequest(t *testing.T, repo contract.GitRepository, profile contract.GitRoutingProfile, ref string) *gitwire.Request {
	t.Helper()
	target, err := httppolicy.ParseRequest(repo.URL+"/git-receive-pack", "POST", "example.com", "", nil)
	require.NoError(t, err)
	control := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " " + ref
	request, err := gitwire.New(t.Context(), target, repo, profile.Revision, http.Header{"Content-Type": {"application/x-git-receive-pack-request"}}, io.NopCloser(strings.NewReader(fmt.Sprintf("%04x%s0000PACKopaque", len(control)+4, control))))
	require.NoError(t, err)
	return request
}
func TestGitExactRequestCandidateAndPolicyConfirmation(t *testing.T) {
	for _, change := range []string{"unchanged", "substitution", "repository", "profile", "revocation", "cancel", "original cancellation", "drain"} {
		t.Run(change, func(t *testing.T) {
			r, _ := newRepository(t, nil)
			principal, credential := createAdmissionCredential(t, r)
			repo, err := r.PutGitRepository(t.Context(), "", "", gitRepositoryInput())
			require.NoError(t, err)
			profile, err := r.GetGitRoutingProfile(t.Context())
			require.NoError(t, err)
			profile, err = r.PutGitRoutingProfile(t.Context(), profile.Revision, []string{"https://example.com"})
			require.NoError(t, err)
			grant, err := r.PutGitGrant(t.Context(), "", "", GitGrantInput{PrincipalID: principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(gitWritePolicy)})
			require.NoError(t, err)
			lease := mustAuthenticateLease(t, r, credential.Bearer)
			defer lease.Release()
			original := gitRequest(t, repo, profile, "refs/heads/team/a")
			originalContext, originalCancel := context.WithCancel(t.Context())
			defer originalCancel()
			evaluation, err := r.EvaluateGitAdmission(originalContext, lease, id(88), formatAuthorizationTime(testNow), original, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}})
			require.NoError(t, err)
			require.NotNil(t, evaluation.Candidate)
			request := original
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch change {
			case "substitution":
				request = gitRequest(t, repo, profile, "refs/heads/team/b")
				substitute, err := r.EvaluateGitAdmission(t.Context(), lease, id(88), formatAuthorizationTime(testNow), request, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}})
				require.NoError(t, err)
				require.NotEqual(t, evaluation.Evidence.RefEvidence, substitute.Evidence.RefEvidence)
				// Even identical retained evidence cannot replace the exact request owner.
				substitute.Evidence.RefEvidence = evaluation.Evidence.RefEvidence
				require.Equal(t, evaluation.Evidence, substitute.Evidence)
			case "repository":
				def := repo.GitRepositoryDefinition
				def.Name = "edited"
				_, err = r.PutGitRepository(t.Context(), repo.ID, repo.Revision, def)
				require.NoError(t, err)
			case "profile":
				_, err = r.PutGitRoutingProfile(t.Context(), profile.Revision, []string{})
				require.NoError(t, err)
			case "revocation":
				require.NoError(t, r.DeleteGitGrant(t.Context(), grant.ID, grant.Revision))
			case "cancel":
				cancel()
			case "original cancellation":
				originalCancel()
			case "drain":
				r.BeginDrain()
			}
			err = r.ConfirmGit(ctx, evaluation.Candidate, id(88), request, nil)
			if change == "unchanged" {
				require.NoError(t, err)
				require.Equal(t, leaseAdmitted, leasePhase(lease.phase.Load()))
				require.Error(t, r.ConfirmGit(ctx, evaluation.Candidate, id(88), original, nil))
			} else {
				require.Error(t, err)
				require.NotEqual(t, leaseAdmitted, leasePhase(lease.phase.Load()))
			}
		})
	}
}
func TestGitActivationBetweenClassificationAndHTTPEvaluation(t *testing.T) {
	r, _ := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, r)
	_, err := r.PutHTTPGrant(t.Context(), "", "", HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(allowHTTPPolicy)})
	require.NoError(t, err)
	target, err := httppolicy.ParseRequest("https://example.com/owner/repo/git-receive-pack", "POST", "example.com", "", nil)
	require.NoError(t, err)
	_, profile, shaped, err := r.ResolveGitRequest(t.Context(), target)
	require.NoError(t, err)
	require.False(t, shaped)
	lease := mustAuthenticateLease(t, r, credential.Bearer)
	defer lease.Release()
	_, err = r.PutGitRoutingProfile(t.Context(), profile.Revision, []string{"https://example.com"})
	require.NoError(t, err)
	evaluation, err := r.EvaluateHTTPAdmission(t.Context(), lease, id(88), formatAuthorizationTime(testNow), HTTPAccessInput{PrincipalID: principal.ID, URL: target.URL().String(), Method: "POST"}, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}})
	require.ErrorIs(t, err, ErrAuthorizationUnavailable)
	require.Nil(t, evaluation.Candidate)
	err = r.ConfirmHTTP(t.Context(), evaluation.Candidate, id(88), nil)
	require.Error(t, err)
	require.NotEqual(t, leaseAdmitted, leasePhase(lease.phase.Load()))
}

func TestGitActivationAdmissionRace(t *testing.T) {
	r, _ := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, r)
	_, err := r.PutHTTPGrant(t.Context(), "", "", HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(`{"version":1,"type":"allow_tunnel","destination":{"host":"example.com","port":443}}`)})
	require.NoError(t, err)
	facts := httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
	for i := range 16 {
		profile, err := r.GetGitRoutingProfile(t.Context())
		require.NoError(t, err)
		profile, err = r.PutGitRoutingProfile(t.Context(), profile.Revision, []string{})
		require.NoError(t, err)
		lease := mustAuthenticateLease(t, r, credential.Bearer)
		identity := id(100 + i)
		evaluation, err := r.EvaluateHTTPAdmission(t.Context(), lease, identity, formatAuthorizationTime(testNow), HTTPAccessInput{PrincipalID: principal.ID, Connect: &contract.HTTPDestinationSelector{Host: "example.com", Port: 443}}, facts)
		require.NoError(t, err)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var activation, confirmation error
		go func() {
			defer wg.Done()
			<-start
			_, activation = r.PutGitRoutingProfile(t.Context(), profile.Revision, []string{"https://example.com"})
		}()
		go func() {
			defer wg.Done()
			<-start
			confirmation = r.ConfirmHTTP(t.Context(), evaluation.Candidate, identity, nil)
		}()
		close(start)
		wg.Wait()
		require.NotEqual(t, activation == nil, confirmation == nil, "exactly one competing transition must win")
		if confirmation == nil {
			r.ReleaseOpaque(evaluation.Candidate)
		}
		lease.Release()
	}
}
func TestGitActivationFencesActualOpaqueOwner(t *testing.T) {
	r, _ := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, r)
	_, err := r.PutHTTPGrant(t.Context(), "", "", HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(`{"version":1,"type":"allow_tunnel","destination":{"host":"example.com","port":443}}`)})
	require.NoError(t, err)
	lease := mustAuthenticateLease(t, r, credential.Bearer)
	defer lease.Release()
	input := HTTPAccessInput{PrincipalID: principal.ID, Connect: &contract.HTTPDestinationSelector{Host: "example.com", Port: 443}}
	facts := httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
	evaluation, err := r.EvaluateHTTPAdmission(t.Context(), lease, id(88), formatAuthorizationTime(testNow), input, facts)
	require.NoError(t, err)
	require.NotNil(t, evaluation.Candidate)
	require.NoError(t, r.ConfirmHTTP(t.Context(), evaluation.Candidate, id(88), nil))
	profile, err := r.GetGitRoutingProfile(t.Context())
	require.NoError(t, err)
	_, err = r.PutGitRoutingProfile(t.Context(), profile.Revision, []string{"https://example.com"})
	require.ErrorIs(t, err, ErrConflict)
	r.ReleaseOpaque(evaluation.Candidate)
	profile, err = r.PutGitRoutingProfile(t.Context(), profile.Revision, []string{"https://example.com"})
	require.NoError(t, err)
	require.True(t, profile.Active)
	next := mustAuthenticateLease(t, r, credential.Bearer)
	defer next.Release()
	_, err = r.EvaluateHTTPAdmission(t.Context(), next, id(89), formatAuthorizationTime(testNow), input, facts)
	require.Error(t, err)
}
