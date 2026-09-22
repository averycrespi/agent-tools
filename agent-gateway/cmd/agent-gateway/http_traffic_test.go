package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestCLIHTTPTrafficSeparateReadOnlyHistory(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v2/http/traffic", r.URL.Path)
		require.Equal(t, "example.com", r.URL.Query().Get("destination"))
		require.Equal(t, "request", r.URL.Query().Get("type"))
		require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("If-Match"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Empty(t, body)
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		require.NoError(t, json.NewEncoder(w).Encode(contract.HTTPTrafficPage{Items: []contract.HTTPTrafficSummary{}}))
	}))
	defer server.Close()
	output, err := executePrincipalRequestETagCommand(t, server.URL, "http", "traffic", "list", "--destination", "example.com", "--type", "request")
	require.NoError(t, err, string(output))
	require.Equal(t, 1, calls)
	require.Contains(t, string(output), `"items":[]`)
}
func TestCLIHTTPTrafficUnknownAndTunnelPresentation(t *testing.T) {
	ref := contract.HTTPRevisionRef{ID: idForSecurityTest(), Revision: 1}
	allowPrivate := false
	at := "2026-09-21T00:00:00.000000000Z"
	item := contract.HTTPTrafficRecord{Admission: contract.HTTPTrafficAdmission{ID: idForSecurityTest(), AdmittedAt: at, EvaluatedAt: at, Principal: ref, AgentCredential: ref, CredentialFingerprint: "0123456789abcdef", Class: "evaluated", Default: contract.HTTPDefaultBlock, Target: &contract.HTTPTrafficTarget{Host: "example.com", Port: 443}, Decision: &contract.HTTPDecision{Version: 1, Principal: ref, PolicyRevision: 1, DefaultRevision: 1, Allowed: true, Transport: contract.HTTPTransportTunnel, Reason: contract.HTTPReasonTunnelAllow, Grant: &ref}, Grants: []contract.HTTPTrafficGrant{{Reference: ref, Policy: contract.HTTPPolicy{Version: 1, Type: contract.HTTPAllowTunnel, Destination: &contract.HTTPDestinationSelector{Host: "example.com", Port: 443}, AllowPrivate: &allowPrivate}}}}}
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	table, err := httpTrafficItemTable(raw)
	require.NoError(t, err)
	require.Contains(t, table.Rows, []string{"Visibility", "Opaque tunnel; no inner-request visibility."})
	require.Contains(t, table.Rows, []string{"Outcome", "Unknown: missing terminal evidence does not prove nonexecution or safe retry."})
	item.Admission.Target.Host = "example.com/private-canary?token=secret"
	raw, err = json.Marshal(item)
	require.NoError(t, err)
	_, err = httpTrafficItemTable(raw)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-canary")
}
