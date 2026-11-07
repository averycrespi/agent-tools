//go:build integration

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestIntegrationResourceKeysKeepOrdinaryIDAPIsCompatible(t *testing.T) {
	for _, domain := range []string{"http", "git"} {
		t.Run(domain, func(t *testing.T) {
			var h http.Handler
			if domain == "http" {
				h, _ = newHTTPCredentialIntegrationHandler(t)
			} else {
				h = newGitIntegrationHandler(t)
			}
			headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
			principal := perform(h, http.MethodPost, "/api/v2/principals", `{"key":"portable","display_name":"Agent","visibility":"all"}`, headers)
			require.Equal(t, 201, principal.Code, principal.Body.String())
			var a contract.PrincipalCreation
			require.NoError(t, json.Unmarshal(principal.Body.Bytes(), &a))
			require.Equal(t, "portable", a.Principal.Key)
			require.True(t, contract.ValidResourceKey(a.DefaultGrant.Key))
			get := perform(h, http.MethodGet, "/api/v2/principals/"+a.Principal.ID, "", headers)
			require.Equal(t, 200, get.Code)
			require.Contains(t, get.Body.String(), `"key":"portable"`)
			patchHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": principal.Header().Get("ETag")}
			require.Equal(t, 400, perform(h, http.MethodPatch, "/api/v2/principals/"+a.Principal.ID, `{"key":"other","display_name":"Changed"}`, patchHeaders).Code)
			specs := []struct{ path, body, deleteBody string }{}
			if domain == "http" {
				specs = append(specs, struct{ path, body, deleteBody string }{"/api/v2/http/credentials", httpCredentialCreateBody, "{}"})
				specs = append(specs, struct{ path, body, deleteBody string }{"/api/v2/http/grants", fmt.Sprintf(`{"principal_id":%q,"description":null,"expires_at":null,"policy":{"version":1,"type":"block_destination","destination":{"host":"example.com","port":443}}}`, a.Principal.ID), ""})
			} else {
				repo := perform(h, http.MethodPost, "/api/v2/git/repositories", `{"key":"portable","name":"Repo","url":"https://example.com/team/repo","aliases":[],"credential_id":null}`, headers)
				require.Equal(t, 201, repo.Code, repo.Body.String())
				var r contract.GitRepository
				require.NoError(t, json.Unmarshal(repo.Body.Bytes(), &r))
				require.Equal(t, "portable", r.Key)
				specs = append(specs, struct{ path, body, deleteBody string }{"/api/v2/git/credentials", `{"name":"Git","origin":"https://example.com","recipe":{"header":"Authorization","prefix":"Bearer "},"secret":"synthetic-resource-key-test"}`, "{}"})
				specs = append(specs, struct{ path, body, deleteBody string }{"/api/v2/git/grants", fmt.Sprintf(`{"principal_id":%q,"repository_id":%q,"description":null,"expires_at":null,"policy":{"version":1,"read":true,"refs":[]}}`, a.Principal.ID, r.ID), ""})
			}
			for _, spec := range specs {
				t.Run(spec.path, func(t *testing.T) {
					for _, key := range []string{`null`, `""`, `"Upper"`, `42`} {
						bad := `{"key":` + key + `,` + spec.body[1:]
						require.Equal(t, 400, perform(h, http.MethodPost, spec.path, bad, headers).Code, bad)
					}
					explicit := `{"key":"portable",` + spec.body[1:]
					created := perform(h, http.MethodPost, spec.path, explicit, headers)
					require.Equal(t, 201, created.Code, created.Body.String())
					var resource struct {
						Key string `json:"key"`
						ID  string `json:"id"`
					}
					require.NoError(t, json.Unmarshal(created.Body.Bytes(), &resource))
					require.Equal(t, "portable", resource.Key)
					read := perform(h, http.MethodGet, spec.path+"/"+resource.ID, "", headers)
					require.Equal(t, 200, read.Code)
					require.Contains(t, read.Body.String(), `"key":"portable"`)
					require.Equal(t, created.Header().Get("ETag"), read.Header().Get("ETag"))
					conditional := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": created.Header().Get("ETag")}
					require.Equal(t, 400, perform(h, http.MethodPatch, spec.path+"/"+resource.ID, `{"key":"other"}`, conditional).Code)
					require.Equal(t, 204, perform(h, http.MethodDelete, spec.path+"/"+resource.ID, spec.deleteBody, conditional).Code)
					conflict := perform(h, http.MethodPost, spec.path, explicit, headers)
					require.Equal(t, 409, conflict.Code, conflict.Body.String())
					require.NotContains(t, conflict.Body.String(), "portable")
					generated := perform(h, http.MethodPost, spec.path, spec.body, headers)
					require.Equal(t, 201, generated.Code, generated.Body.String())
					require.NoError(t, json.Unmarshal(generated.Body.Bytes(), &resource))
					require.True(t, contract.ValidResourceKey(resource.Key))
					require.True(t, strings.Contains(resource.Key, "-"))
				})
			}
		})
	}
}
