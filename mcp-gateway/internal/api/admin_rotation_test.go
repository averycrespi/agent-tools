package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rotationCredentials struct {
	items     []contract.AdminCredential
	authority string
	bearer    string
}

func (credentials *rotationCredentials) Authenticate(_ context.Context, bearer string) (contract.AdminCredential, error) {
	if bearer != testBearer {
		return contract.AdminCredential{}, admin.ErrAuthenticationRequired
	}
	return credentials.items[0], nil
}

func (credentials *rotationCredentials) Authority(context.Context) (contract.AdminAuthority, error) {
	return contract.AdminAuthority{Revision: credentials.authority}, nil
}

func (credentials *rotationCredentials) Create(_ context.Context, expires *time.Time) (contract.CreatedAdminCredential, error) {
	return credentials.create(expires), nil
}

func (credentials *rotationCredentials) CreateConditional(_ context.Context, expires *time.Time, expected string) (contract.CreatedAdminCredential, error) {
	if expected != credentials.authority {
		return contract.CreatedAdminCredential{}, admin.ErrStaleAuthority
	}
	return credentials.create(expires), nil
}

func (credentials *rotationCredentials) create(expires *time.Time) contract.CreatedAdminCredential {
	item := credential()
	item.ID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	item.Revision = "2"
	if expires != nil {
		value := expires.UTC().Format(time.RFC3339Nano)
		item.ExpiresAt, item.NonExpiring = &value, false
	}
	credentials.items = append(credentials.items, item)
	credentials.authority = item.Revision
	return contract.CreatedAdminCredential{AdminCredential: item, Bearer: credentials.bearer}
}

func (credentials *rotationCredentials) Get(_ context.Context, id string) (contract.AdminCredential, error) {
	for _, item := range credentials.items {
		if item.ID == id {
			return item, nil
		}
	}
	return contract.AdminCredential{}, admin.ErrNotFound
}

func (credentials *rotationCredentials) List(context.Context) ([]contract.AdminCredential, error) {
	return append([]contract.AdminCredential(nil), credentials.items...), nil
}

func (credentials *rotationCredentials) Revoke(context.Context, string) error { return nil }

func (credentials *rotationCredentials) CompleteRotation(_ context.Context, oldID, replacementID, expected string) (contract.AdminCredentialRotationResult, error) {
	if expected != credentials.authority {
		return contract.AdminCredentialRotationResult{}, admin.ErrStaleAuthority
	}
	if oldID == replacementID || len(credentials.items) != 2 || credentials.items[0].Status != contract.CredentialActive || credentials.items[1].ID != replacementID || credentials.items[1].Status != contract.CredentialActive || !credentials.items[1].NonExpiring {
		return contract.AdminCredentialRotationResult{}, admin.ErrRotationConflict
	}
	old := credentials.items[0]
	old.Status, old.Revision = contract.CredentialRevoked, "3"
	credentials.items[0] = old
	credentials.authority = old.Revision
	return contract.AdminCredentialRotationResult{OldCredential: old, NewCredential: credentials.items[1]}, nil
}

func TestAdminCredentialRotationAPI(t *testing.T) {
	credentials := &rotationCredentials{items: []contract.AdminCredential{credential()}, authority: "1", bearer: "mgw_admin_ONETIME"}
	handler := New(Options{Credentials: credentials, Sessions: fakeSessions{}})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	auth := map[string]string{"Authorization": "Bearer " + testBearer}
	jsonAuth := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}

	authority := perform(boundary, http.MethodGet, "/api/v1/admin-authority", "", auth)
	require.Equal(t, http.StatusOK, authority.Code, authority.Body.String())
	assert.Equal(t, contract.AdminAuthorityETag("1"), authority.Header().Get("ETag"))
	assert.JSONEq(t, `{"revision":"1"}`, authority.Body.String())

	sessionOnly := perform(boundary, http.MethodGet, "/api/v1/admin-authority", "", map[string]string{"Cookie": contract.SessionCookieName + "=session"})
	assert.Equal(t, http.StatusUnauthorized, sessionOnly.Code)

	unconditional := perform(boundary, http.MethodPost, "/api/v1/admin-credentials", `{"expires_at":null}`, jsonAuth)
	require.Equal(t, http.StatusCreated, unconditional.Code, unconditional.Body.String())
	assert.Empty(t, unconditional.Header().Get("ETag"))
	credentials.items = credentials.items[:1]
	credentials.authority = "1"

	sessionConditional := perform(boundary, http.MethodPost, "/api/v1/admin-credentials", `{"expires_at":null}`, map[string]string{
		"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin, "X-CSRF-Token": "csrf", "Content-Type": contract.MediaTypeJSON, "If-Match": contract.AdminAuthorityETag("1"),
	})
	assert.Equal(t, http.StatusUnauthorized, sessionConditional.Code)
	assert.Len(t, credentials.items, 1)

	conditionalHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.AdminAuthorityETag("1")}
	conditional := perform(boundary, http.MethodPost, "/api/v1/admin-credentials", `{"expires_at":null}`, conditionalHeaders)
	require.Equal(t, http.StatusCreated, conditional.Code, conditional.Body.String())
	assert.Equal(t, contract.AdminAuthorityETag("2"), conditional.Header().Get("ETag"))
	assert.Contains(t, conditional.Body.String(), credentials.bearer)

	staleHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.AdminAuthorityETag("1")}
	stale := perform(boundary, http.MethodPost, "/api/v1/admin-credentials", `{"expires_at":null}`, staleHeaders)
	assert.Equal(t, http.StatusPreconditionFailed, stale.Code)
	assert.Contains(t, stale.Body.String(), string(contract.ProblemStaleAdminAuthority))
	assert.Len(t, credentials.items, 2)

	missing := perform(boundary, http.MethodPost, "/api/v1/admin-credentials/"+testID+"/rotation-completion", `{"replacement_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}`, jsonAuth)
	assert.Equal(t, http.StatusPreconditionRequired, missing.Code)
	staleCompletionHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.AdminAuthorityETag("1")}
	staleCompletion := perform(boundary, http.MethodPost, "/api/v1/admin-credentials/"+testID+"/rotation-completion", `{"replacement_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}`, staleCompletionHeaders)
	assert.Equal(t, http.StatusPreconditionFailed, staleCompletion.Code)
	completionHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.AdminAuthorityETag("2")}
	completed := perform(boundary, http.MethodPost, "/api/v1/admin-credentials/"+testID+"/rotation-completion", `{"replacement_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}`, completionHeaders)
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
	assert.Equal(t, contract.AdminAuthorityETag("3"), completed.Header().Get("ETag"))
	assert.NotContains(t, completed.Body.String(), credentials.bearer)
	assert.Contains(t, completed.Body.String(), `"status":"revoked"`)
	conflictHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.AdminAuthorityETag("3")}
	conflict := perform(boundary, http.MethodPost, "/api/v1/admin-credentials/"+testID+"/rotation-completion", `{"replacement_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}`, conflictHeaders)
	assert.Equal(t, http.StatusConflict, conflict.Code)
	assert.Contains(t, conflict.Body.String(), string(contract.ProblemAdminRotationConflict))

	for _, invalid := range []struct {
		name   string
		etag   string
		body   string
		status int
	}{
		{name: "weak", etag: `W/"admin-authority-3"`, body: `{"replacement_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}`, status: http.StatusPreconditionFailed},
		{name: "unknown member", etag: contract.AdminAuthorityETag("3"), body: `{"replacement_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW","extra":true}`, status: http.StatusBadRequest},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": invalid.etag}
			response := perform(boundary, http.MethodPost, "/api/v1/admin-credentials/"+testID+"/rotation-completion", invalid.body, headers)
			assert.Equal(t, invalid.status, response.Code, response.Body.String())
		})
	}
}
