package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminAuthorityRotationContract(t *testing.T) {
	t.Parallel()

	authorityRoute, ok := RouteForPath("/api/v1/admin-authority")
	require.True(t, ok)
	require.Equal(t, Route{Pattern: "/api/v1/admin-authority", Methods: []string{"GET"}, Authority: AuthorityAdminBearer}, authorityRoute)
	completionRoute, ok := RouteForPath("/api/v1/admin-credentials/01ARZ3NDEKTSV4RRFFQ69G5FAV/rotation-completion")
	require.True(t, ok)
	require.Equal(t, Route{Pattern: "/api/v1/admin-credentials/{id}/rotation-completion", Methods: []string{"POST"}, Authority: AuthorityAdminBearer}, completionRoute)

	mechanics := ResourceMechanics()
	require.Contains(t, mechanics, ResourceMechanic{Pattern: "/api/v1/admin-authority", Method: "GET", RequestSchema: "None", SuccessSchema: "AdminAuthority", SuccessStatuses: []int{200}, ETag: true})
	require.Contains(t, mechanics, ResourceMechanic{Pattern: "/api/v1/admin-credentials", Method: "POST", RequestSchema: "AdminCredentialCreate", SuccessSchema: "CreatedAdminCredential", SuccessStatuses: []int{201}, OptionalPrecondition: true, ETag: true})
	require.Contains(t, mechanics, ResourceMechanic{Pattern: "/api/v1/admin-credentials/{id}/rotation-completion", Method: "POST", RequestSchema: "AdminCredentialRotationCompletion", SuccessSchema: "AdminCredentialRotationResult", SuccessStatuses: []int{200}, Precondition: true, ETag: true})

	for _, expected := range []Problem{
		{Status: 409, Code: ProblemAdminRotationConflict, Title: "The administrator credential rotation conflicts with current state."},
		{Status: 412, Code: ProblemStaleAdminAuthority, Title: "The administrator authority revision is stale."},
		{Status: 428, Code: ProblemAdminAuthorityPreconditionRequired, Title: "The administrator authority revision is required."},
	} {
		actual, found := ProblemForCode(expected.Code)
		require.True(t, found)
		require.Equal(t, expected, actual)
	}

	etag := AdminAuthorityETag("7")
	require.Equal(t, `"admin-authority-7"`, etag)
	require.True(t, MatchesAdminAuthorityETag(etag, "7"))
	for _, invalid := range []string{"", "*", `W/` + etag, etag + ", " + etag, `"admin-authority-07"`, `"admin-authority-8"`} {
		require.False(t, MatchesAdminAuthorityETag(invalid, "7"), invalid)
	}

	authorityJSON, err := json.Marshal(AdminAuthority{Revision: "7"})
	require.NoError(t, err)
	require.JSONEq(t, `{"revision":"7"}`, string(authorityJSON))
	completionJSON, err := json.Marshal(AdminCredentialRotationCompletion{ReplacementID: "replacement"})
	require.NoError(t, err)
	require.JSONEq(t, `{"replacement_id":"replacement"}`, string(completionJSON))
	requireJSONKeys(t, AdminCredentialRotationResult{OldCredential: AdminCredential{ID: "old"}, NewCredential: AdminCredential{ID: "new"}}, "old_credential", "new_credential")
}
