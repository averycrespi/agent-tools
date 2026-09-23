//go:build integration

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestIntegrationHTTPGrantCredentialMutationRaces(t *testing.T) {
	for _, operation := range []string{http.MethodPatch, http.MethodDelete} {
		t.Run(operation, func(t *testing.T) {
			handler, _ := newHTTPCredentialIntegrationHandler(t)
			headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
			p := perform(handler, http.MethodPost, "/api/v2/principals", `{"display_name":"HTTP race","visibility":"all"}`, headers)
			require.Equal(t, 201, p.Code)
			var principal contract.PrincipalCreation
			require.NoError(t, json.Unmarshal(p.Body.Bytes(), &principal))
			c := perform(handler, http.MethodPost, "/api/v2/http/credentials", httpCredentialCreateBody, headers)
			require.Equal(t, 201, c.Code)
			var credential contract.HTTPCredential
			require.NoError(t, json.Unmarshal(c.Body.Bytes(), &credential))
			body := fmt.Sprintf(`{"principal_id":%q,"description":null,"expires_at":null,"policy":{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"api.example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}},"credential_id":%q}}`, principal.Principal.ID, credential.ID)
			updateHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": c.Header().Get("ETag")}
			updateBody := `{"name":"Narrowed","boundary":{"host":"other.example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "}}`
			if operation == http.MethodDelete {
				updateBody = `{}`
			}
			start := make(chan struct{})
			grants := make(chan *httptest.ResponseRecorder, 1)
			changes := make(chan *httptest.ResponseRecorder, 1)
			go func() { <-start; grants <- perform(handler, http.MethodPost, "/api/v2/http/grants", body, headers) }()
			go func() {
				<-start
				changes <- perform(handler, operation, "/api/v2/http/credentials/"+credential.ID, updateBody, updateHeaders)
			}()
			close(start)
			g, changed := <-grants, <-changes
			// The bounded writer can reject the losing concurrent request before
			// admission (429). One explicit fresh attempt after that known refusal
			// proves the committed winner makes the incompatible change fail closed.
			if g.Code == 201 {
				if changed.Code == 429 {
					changed = perform(handler, operation, "/api/v2/http/credentials/"+credential.ID, updateBody, updateHeaders)
				}
				require.Equal(t, 409, changed.Code, changed.Body.String())
			} else {
				require.Contains(t, []int{200, 204}, changed.Code, changed.Body.String())
				if g.Code == 429 {
					g = perform(handler, http.MethodPost, "/api/v2/http/grants", body, headers)
				}
				require.Equal(t, 409, g.Code, g.Body.String())
			}
		})
	}
}

func TestIntegrationHTTPGrantsReferencesDefaultsAndPreview(t *testing.T) {
	handler, events := newHTTPCredentialIntegrationHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	principalResponse := perform(handler, http.MethodPost, "/api/v2/principals", `{"display_name":"HTTP Agent","visibility":"all"}`, headers)
	require.Equal(t, 201, principalResponse.Code, principalResponse.Body.String())
	var principal contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(principalResponse.Body.Bytes(), &principal))
	defaultPath := "/api/v2/principals/" + principal.Principal.ID
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		retired := perform(handler, method, "/api/v2/http/defaults/"+principal.Principal.ID, `{"default":"allow"}`, headers)
		require.Equal(t, http.StatusNotFound, retired.Code)
	}
	def := perform(handler, http.MethodGet, defaultPath, "", headers)
	require.Equal(t, 200, def.Code)
	require.Contains(t, def.Body.String(), `"http_default":"block"`)
	headers["If-Match"] = def.Header().Get("ETag")
	changed := perform(handler, http.MethodPatch, defaultPath, `{"http_default":"allow"}`, headers)
	require.Equal(t, 200, changed.Code, changed.Body.String())
	require.Equal(t, 412, perform(handler, http.MethodPatch, defaultPath, `{"http_default":"block"}`, headers).Code)
	delete(headers, "If-Match")
	created := perform(handler, http.MethodPost, "/api/v2/http/credentials", httpCredentialCreateBody, headers)
	require.Equal(t, 201, created.Code, created.Body.String())
	var credential contract.HTTPCredential
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &credential))
	body := fmt.Sprintf(`{"principal_id":%q,"description":"API access","expires_at":null,"policy":{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":%q,"port":%d},"methods":{"any":true},"path":{"kind":"any"}},"credential_id":%q}}`, principal.Principal.ID, credential.Boundary.Host, credential.Boundary.Port, credential.ID)
	granted := perform(handler, http.MethodPost, "/api/v2/http/grants", body, headers)
	require.Equal(t, 201, granted.Code, granted.Body.String())
	var grant contract.HTTPGrant
	require.NoError(t, json.Unmarshal(granted.Body.Bytes(), &grant))
	read := perform(handler, http.MethodGet, "/api/v2/http/credentials/"+credential.ID, "", headers)
	require.Contains(t, read.Body.String(), grant.ID)
	headers["If-Match"] = created.Header().Get("ETag")
	refused := perform(handler, http.MethodDelete, "/api/v2/http/credentials/"+credential.ID, `{}`, headers)
	require.Equal(t, 409, refused.Code, refused.Body.String())
	update := fmt.Sprintf(`{"name":"changed","boundary":{"host":%q,"port":%d,"allow_wildcard":%t},"recipe":{"header":"X-Other-Key","prefix":""}}`, credential.Boundary.Host, credential.Boundary.Port, credential.Boundary.AllowWildcard)
	require.Equal(t, 409, perform(handler, http.MethodPatch, "/api/v2/http/credentials/"+credential.ID, update, headers).Code)
	delete(headers, "If-Match")
	beforeEvents := len(*events)
	previewBody := fmt.Sprintf(`{"principal_id":%q,"url":%q,"method":"GET"}`, principal.Principal.ID, fmt.Sprintf("https://%s:%d/path?canary=private-preview", credential.Boundary.Host, credential.Boundary.Port))
	preview := perform(handler, http.MethodPost, "/api/v2/http/access-preview", previewBody, headers)
	require.Equal(t, 200, preview.Code, preview.Body.String())
	require.Contains(t, preview.Body.String(), `"policy_only":true`)
	require.Contains(t, preview.Body.String(), credential.ID)
	require.NotContains(t, preview.Body.String(), "private-preview")
	require.Equal(t, beforeEvents, len(*events))
	require.Equal(t, "no-store", preview.Header().Get("Cache-Control"))
	for _, bad := range []string{
		fmt.Sprintf(`{"principal_id":%q,"connect":{"host":"example.com","port":443,"extra":true}}`, principal.Principal.ID),
		fmt.Sprintf(`{"principal_id":%q,"connect":null}`, principal.Principal.ID),
		fmt.Sprintf(`{"principal_id":%q,"connect":{"host":"example.com","port":443},"url":null}`, principal.Principal.ID),
	} {
		require.Equal(t, 400, perform(handler, http.MethodPost, "/api/v2/http/access-preview", bad, headers).Code)
	}
	path := "/api/v2/http/grants/" + grant.ID
	require.Equal(t, 428, perform(handler, http.MethodPatch, path, body, headers).Code)
	headers["If-Match"] = granted.Header().Get("ETag")
	edited := perform(handler, http.MethodPatch, path, body, headers)
	require.Equal(t, 200, edited.Code, edited.Body.String())
	require.Equal(t, 412, perform(handler, http.MethodDelete, path, "", headers).Code)
	headers["If-Match"] = edited.Header().Get("ETag")
	require.Equal(t, 204, perform(handler, http.MethodDelete, path, "", headers).Code)
}
