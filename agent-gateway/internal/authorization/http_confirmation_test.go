package authorization

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestHTTPConfirmationOriginalContextAndOpaqueOwnership(t *testing.T) {
	for _, change := range []string{"unchanged", "original cancellation", "drain", "revision"} {
		t.Run(change, func(t *testing.T) {
			r, _ := newRepository(t, nil)
			principal, credential := createAdmissionCredential(t, r)
			_, err := r.PutHTTPGrant(t.Context(), "", "", HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(`{"version":1,"type":"allow_tunnel","destination":{"host":"example.com","port":443}}`)})
			require.NoError(t, err)
			lease := mustAuthenticateLease(t, r, credential.Bearer)
			defer lease.Release()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			input := HTTPAccessInput{PrincipalID: principal.ID, Connect: &contract.HTTPDestinationSelector{Host: "example.com", Port: 443}}
			facts := httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
			evaluation, err := r.EvaluateHTTPAdmission(ctx, lease, "", "", input, facts)
			require.NoError(t, err)
			require.NotNil(t, evaluation.Candidate)
			// A never-confirmed candidate cannot release another actual owner's count.
			r.authority.opaqueGitOrigins["https://example.com:443"] = 1
			r.ReleaseOpaque(evaluation.Candidate)
			require.Equal(t, 1, r.authority.opaqueGitOrigins["https://example.com:443"])
			switch change {
			case "original cancellation":
				cancel()
			case "drain":
				r.BeginDrain()
			case "revision":
				_, err = r.CreatePrincipal(t.Context(), CreatePrincipalRequest{DisplayName: "other", Visibility: contract.VisibilityAll})
				require.NoError(t, err)
			}
			err = r.ConfirmHTTP(t.Context(), evaluation.Candidate, "", nil)
			if change == "unchanged" {
				require.NoError(t, err)
				require.Equal(t, 2, r.authority.opaqueGitOrigins["https://example.com:443"])
				r.ReleaseOpaque(evaluation.Candidate)
				r.ReleaseOpaque(evaluation.Candidate)
				require.Equal(t, 1, r.authority.opaqueGitOrigins["https://example.com:443"])
			} else {
				require.Error(t, err)
				r.ReleaseOpaque(evaluation.Candidate)
				require.Equal(t, 1, r.authority.opaqueGitOrigins["https://example.com:443"])
			}
			require.Error(t, r.ConfirmHTTP(t.Context(), evaluation.Candidate, "", nil))
		})
	}
}
