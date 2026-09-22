package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func httpResponseGrantFixture() contract.HTTPGrant {
	description := "response-private-canary"
	return contract.HTTPGrant{ID: idForSecurityTest(), PrincipalID: idForSecurityTest(), Description: &description, Revision: "2", State: contract.GrantActive, CreatedAt: "2026-09-21T00:00:00.000000000Z", UpdatedAt: "2026-09-21T00:00:01.000000000Z", Policy: json.RawMessage(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"values":["GET","POST"]},"path":{"kind":"segment_prefix","value":"/v1"}},"allow_private":false}`)}
}

func TestCLIHTTPGrantMalformedSuccessCannotAcknowledge(t *testing.T) {
	policy := func(edit func(*contract.HTTPPolicy)) func(*contract.HTTPGrant) {
		return func(g *contract.HTTPGrant) {
			var p contract.HTTPPolicy
			require.NoError(t, json.Unmarshal(g.Policy, &p))
			edit(&p)
			raw, err := json.Marshal(p)
			require.NoError(t, err)
			g.Policy = raw
		}
	}
	cases := map[string]func(*contract.HTTPGrant){
		"description-bound":      func(g *contract.HTTPGrant) { v := strings.Repeat("x", 257); g.Description = &v },
		"description-control":    func(g *contract.HTTPGrant) { v := "invalid\nlabel"; g.Description = &v },
		"created-time":           func(g *contract.HTTPGrant) { g.CreatedAt = "yesterday" },
		"missing-nanoseconds":    func(g *contract.HTTPGrant) { g.UpdatedAt = "2026-09-21T00:00:01Z" },
		"reversed-times":         func(g *contract.HTTPGrant) { g.UpdatedAt = "2026-09-20T23:59:59.000000000Z" },
		"noncanonical-time":      func(g *contract.HTTPGrant) { g.UpdatedAt = "2026-09-21T00:00:01+00:00" },
		"expired-without-expiry": func(g *contract.HTTPGrant) { g.State = contract.GrantExpired },
		"invalid-expiry":         func(g *contract.HTTPGrant) { v := "invalid"; g.ExpiresAt = &v },
		"policy-version":         policy(func(p *contract.HTTPPolicy) { p.Version = 2 }),
		"missing-private":        policy(func(p *contract.HTTPPolicy) { p.AllowPrivate = nil }),
		"host-case":              policy(func(p *contract.HTTPPolicy) { p.Request.Origin.Host = "EXAMPLE.com" }),
		"host-bound":             policy(func(p *contract.HTTPPolicy) { p.Request.Origin.Host = strings.Repeat("a", 254) }),
		"numeric-host":           policy(func(p *contract.HTTPPolicy) { p.Request.Origin.Host = "127.1" }),
		"mapped-ip":              policy(func(p *contract.HTTPPolicy) { p.Request.Origin.Host = "::ffff:127.0.0.1" }),
		"wildcard-ip":            policy(func(p *contract.HTTPPolicy) { p.Request.Origin.Host = "*.127.0.0.1" }),
		"method-case":            policy(func(p *contract.HTTPPolicy) { p.Request.Methods.Values = []string{"get"} }),
		"method-duplicate":       policy(func(p *contract.HTTPPolicy) { p.Request.Methods.Values = []string{"GET", "GET"} }),
		"method-order":           policy(func(p *contract.HTTPPolicy) { p.Request.Methods.Values = []string{"POST", "GET"} }),
		"method-connect":         policy(func(p *contract.HTTPPolicy) { p.Request.Methods.Values = []string{"CONNECT"} }),
		"path-traversal":         policy(func(p *contract.HTTPPolicy) { p.Request.Path.Value = "/a/../b" }),
		"path-escape":            policy(func(p *contract.HTTPPolicy) { p.Request.Path.Value = "/%61" }),
		"prefix-trailing":        policy(func(p *contract.HTTPPolicy) { p.Request.Path.Value = "/v1/" }),
		"path-bound":             policy(func(p *contract.HTTPPolicy) { p.Request.Path.Value = "/" + strings.Repeat("a", 4096) }),
		"http-credential": policy(func(p *contract.HTTPPolicy) {
			id := idForSecurityTest()
			p.CredentialID = &id
			p.Request.Origin.Scheme = "http"
		}),
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			g := httpResponseGrantFixture()
			mutate(&g)
			raw, err := json.Marshal(g)
			require.NoError(t, err)
			for _, mutation := range []bool{false, true} {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", contract.MediaTypeJSON)
					w.Header().Set("ETag", contract.HTTPGrantETag(g.ID, g.Revision))
					_, writeErr := w.Write(raw)
					require.NoError(t, writeErr)
				}))
				args := []string{"http", "grant", "get", g.ID}
				wantCode := "client_response_invalid"
				wantExit := 10
				if mutation {
					input := filepath.Join(t.TempDir(), "grant.json")
					require.NoError(t, os.WriteFile(input, []byte(`{"principal_id":"`+g.ID+`","description":null,"expires_at":null,"policy":`+string(httpResponseGrantFixture().Policy)+`}`), 0o600))
					args = []string{"http", "grant", "update", g.ID, "--etag", contract.HTTPGrantETag(g.ID, "1"), "--yes", "--file", input}
					wantCode = "client_outcome_uncertain"
					wantExit = 8
				}
				output, err := executePrincipalRequestETagCommand(t, server.URL, args...)
				server.Close()
				require.Error(t, err)
				require.Equal(t, wantExit, commandExitCode(err), string(output))
				require.Contains(t, string(output), wantCode)
				require.NotContains(t, string(output), "response-private-canary")
				require.Equal(t, int32(1), calls.Load())
			}
		})
	}
	valid := httpResponseGrantFixture()
	raw, err := json.Marshal(valid)
	require.NoError(t, err)
	for _, bad := range []string{strings.Replace(string(raw), `"description":"response-private-canary",`, "", 1), strings.Replace(string(raw), `"expires_at":null,`, "", 1), strings.Replace(string(raw), `"allow_private":false`, `"allow_private":null`, 1), strings.Replace(string(raw), `"version":1`, `"version":1,"extra":true`, 1)} {
		_, err := httpGrantTable([]byte(bad))
		require.Error(t, err)
	}
}

func TestCLIHTTPGrantAcceptsCanonicalPolicyProjections(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"type":"block_destination","destination":{"host":"EXAMPLE.com","port":443}}`,
		`{"version":1,"type":"allow_tunnel","destination":{"host":"2001:4860:4860::8888","port":443}}`,
		`{"version":1,"type":"block_requests","request":{"origin":{"scheme":"http","host":"127.0.0.1","port":80},"methods":{"any":true},"path":{"kind":"any"}}}`,
		`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"*.example.com","port":443},"methods":{"values":["POST","GET"]},"path":{"kind":"segment_prefix","value":"/%76%31/"}}}`,
	} {
		p, err := httppolicy.DecodePolicy([]byte(raw))
		require.NoError(t, err)
		canonical, err := p.JSON()
		require.NoError(t, err)
		g := httpResponseGrantFixture()
		g.Policy = canonical
		body, err := json.Marshal(g)
		require.NoError(t, err)
		_, err = httpGrantTable(body)
		require.NoError(t, err, string(body))
	}
}

func TestCLIHTTPPreviewAcceptsEvaluatorEvidence(t *testing.T) {
	for _, scenario := range []string{"default-block", "default-allow", "block_destination", "block_requests", "allow_requests", "allow_tunnel", "intercept", "credential", "conflict", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			id := idForSecurityTest()
			origin := contract.HTTPOriginSelector{Scheme: "https", Host: "example.com", Port: 443}
			snapshot := httppolicy.Snapshot{Principal: contract.HTTPRevisionRef{ID: id, Revision: 1}, PolicyRevision: 1, DefaultRevision: 1, Default: contract.HTTPDefaultBlock}
			if scenario == "default-allow" {
				snapshot.Default = contract.HTTPDefaultAllow
			}
			p := contract.HTTPPolicy{Version: 1, Type: contract.HTTPGrantType(scenario)}
			switch scenario {
			case "block_destination", "allow_tunnel":
				p.Destination = &contract.HTTPDestinationSelector{Host: origin.Host, Port: origin.Port}
			case "block_requests", "allow_requests", "credential", "conflict", "unavailable":
				if scenario != "block_requests" {
					p.Type = contract.HTTPAllowRequests
				}
				p.Request = &contract.HTTPRequestSelector{Origin: origin, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}
			}
			if p.Request != nil || p.Destination != nil {
				if scenario == "credential" || scenario == "conflict" || scenario == "unavailable" {
					credentialID := "01ARZ3NDEKTSV4RRFFQ69G5FAA"
					p.CredentialID = &credentialID
					snapshot.Credentials = append(snapshot.Credentials, httppolicy.Credential{Ref: contract.HTTPRevisionRef{ID: credentialID, Revision: 1}, Available: scenario != "unavailable", Origins: []contract.HTTPOriginSelector{origin}})
				}
				compiled, err := httppolicy.Compile(p)
				require.NoError(t, err)
				snapshot.Grants = append(snapshot.Grants, httppolicy.Grant{Ref: contract.HTTPRevisionRef{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Revision: 1}, PrincipalID: id, Policy: compiled})
				if scenario == "conflict" {
					credentialID := "01ARZ3NDEKTSV4RRFFQ69G5FAB"
					p.CredentialID = &credentialID
					snapshot.Credentials = append(snapshot.Credentials, httppolicy.Credential{Ref: contract.HTTPRevisionRef{ID: credentialID, Revision: 1}, Available: true, Origins: []contract.HTTPOriginSelector{origin}})
					compiled, err = httppolicy.Compile(p)
					require.NoError(t, err)
					snapshot.Grants = append(snapshot.Grants, httppolicy.Grant{Ref: contract.HTTPRevisionRef{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Revision: 1}, PrincipalID: id, Policy: compiled})
				}
			}
			evaluator, err := httppolicy.New(snapshot)
			require.NoError(t, err)
			var decision contract.HTTPDecision
			if scenario == "allow_tunnel" || scenario == "intercept" {
				destination, err := httppolicy.NewDestination(origin.Host, origin.Port)
				require.NoError(t, err)
				decision, err = evaluator.ConnectPolicy(destination)
				require.NoError(t, err)
			} else {
				request, err := httppolicy.ParseRequest("https://example.com/", "GET", "example.com", "", nil)
				require.NoError(t, err)
				decision, err = evaluator.RequestPolicy(request)
				require.NoError(t, err)
			}
			body, err := json.Marshal(contract.HTTPAccessPreview{Decision: decision, Default: snapshot.Default, PolicyOnly: true})
			require.NoError(t, err)
			table, err := httpPreviewTable(body)
			require.NoError(t, err, string(body))
			require.Contains(t, table.Headers, "EVIDENCE")
			require.Contains(t, table.Rows[0][4], id)
		})
	}
}

func TestCLIHTTPPreviewRejectsMalformedDecisionEvidence(t *testing.T) {
	fixture := func() contract.HTTPAccessPreview {
		return contract.HTTPAccessPreview{PolicyOnly: true, Default: contract.HTTPDefaultBlock, Decision: contract.HTTPDecision{Version: 1, Principal: contract.HTTPRevisionRef{ID: idForSecurityTest(), Revision: 1}, PolicyRevision: 1, DefaultRevision: 1, Transport: contract.HTTPTransportRequest, Reason: contract.HTTPReasonDefault}}
	}
	cases := map[string]func(*contract.HTTPAccessPreview){
		"default":   func(p *contract.HTTPAccessPreview) { p.Default = "unknown" },
		"transport": func(p *contract.HTTPAccessPreview) { p.Decision.Transport = "unknown" },
		"reason":    func(p *contract.HTTPAccessPreview) { p.Decision.Reason = "unknown" },
		"address-reason-with-block-default": func(p *contract.HTTPAccessPreview) {
			p.Decision.Reason = contract.HTTPReasonAddressForbidden
		},
		"private-reason-with-block-default": func(p *contract.HTTPAccessPreview) {
			p.Decision.Reason = contract.HTTPReasonPrivateRequired
		},
		"principal-revision": func(p *contract.HTTPAccessPreview) { p.Decision.Principal.Revision = 0 },
		"policy-revision":    func(p *contract.HTTPAccessPreview) { p.Decision.PolicyRevision = 0 },
		"default-revision":   func(p *contract.HTTPAccessPreview) { p.Decision.DefaultRevision = 0 },
		"grant-id": func(p *contract.HTTPAccessPreview) {
			p.Decision.Grant = &contract.HTTPRevisionRef{ID: "invalid", Revision: 1}
		},
		"grant-revision": func(p *contract.HTTPAccessPreview) {
			p.Decision.Grant = &contract.HTTPRevisionRef{ID: idForSecurityTest()}
		},
		"unpaired-credential": func(p *contract.HTTPAccessPreview) {
			p.Decision.Credential = &contract.HTTPRevisionRef{ID: idForSecurityTest(), Revision: 1}
		},
		"contradictory-default": func(p *contract.HTTPAccessPreview) { p.Decision.Allowed = true },
		"network-verified":      func(p *contract.HTTPAccessPreview) { p.NetworkVerified = true },
		"other-principal":       func(p *contract.HTTPAccessPreview) { p.Decision.Principal.ID = "01ARZ3NDEKTSV4RRFFQ69G5FAW" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := fixture()
			mutate(&p)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				require.NoError(t, json.NewEncoder(w).Encode(p))
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "preview.json")
			require.NoError(t, os.WriteFile(path, []byte(`{"principal_id":"`+idForSecurityTest()+`","url":"https://example.com/","method":"GET"}`), 0o600))
			output, err := executePrincipalRequestETagCommand(t, server.URL, "http", "test-access", "--file", path)
			require.Error(t, err)
			require.Equal(t, 10, commandExitCode(err), string(output))
			require.Contains(t, string(output), "client_response_invalid")
			require.Contains(t, string(output), `"uncertain":false`)
			require.Equal(t, int32(1), calls.Load())
		})
	}
	raw, err := json.Marshal(fixture())
	require.NoError(t, err)
	for _, bad := range []string{strings.Replace(string(raw), `"network_verified":false,`, "", 1), strings.Replace(string(raw), `"allowed":false`, `"allowed":null`, 1), strings.Replace(string(raw), `"reason":"principal_default"`, `"reason":"principal_default","private_grant":null`, 1)} {
		_, err := httpPreviewTable([]byte(bad))
		require.Error(t, err)
	}
}
