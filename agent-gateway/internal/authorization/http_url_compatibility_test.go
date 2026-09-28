package authorization

import (
	"net/netip"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestHTTPURLPreviewMatchesAdmissionReparse(t *testing.T) {
	r, _ := newRepository(t, nil)
	principal, credential := createAdmissionCredential(t, r)
	policy := `{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"any":true},"path":{"kind":"segment_prefix","value":"/a"}}}`
	_, err := r.PutHTTPGrant(t.Context(), "", "", HTTPGrantInput{PrincipalID: principal.ID, Policy: []byte(policy)})
	require.NoError(t, err)
	lease := mustAuthenticateLease(t, r, credential.Bearer)
	defer lease.Release()
	for _, tc := range []struct {
		target  string
		allowed bool
	}{
		{"/a", true}, {"/%61", true}, {"/a/b", true}, {"/a//b", true}, {"/a/%2fb?x=1&x=2+3", true},
		{"/a/é/%2f?", true}, {"/a%2Fb", false}, {"/a%252Fb", false}, {"/ab", false}, {"//example.com/a", false},
	} {
		input := HTTPAccessInput{PrincipalID: principal.ID, URL: "https://example.com" + tc.target, Method: "GET"}
		preview, err := r.PreviewHTTPAccess(t.Context(), input)
		require.NoError(t, err)
		require.Equal(t, tc.allowed, preview.Decision.Allowed, tc.target)
		parsed, err := httppolicy.ParseRequest(input.URL, input.Method, "example.com", "", nil)
		require.NoError(t, err)
		input.URL = parsed.URL().String()
		admitted, err := r.EvaluateHTTPAccess(t.Context(), lease, input, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}})
		require.NoError(t, err)
		require.Equal(t, preview.Decision, admitted)
		require.Equal(t, contract.HTTPTransportRequest, admitted.Transport)
	}
}
