//go:build e2e

package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func verifyDemoRequestApprovals(t *testing.T, c *client, root string) {
	t.Helper()
	principals := map[string]string{}
	for _, item := range rows(c.get("principals"), "items") {
		row, _ := item.(map[string]any)
		principals[text(row, "id")] = text(row, "display_name")
	}
	expected := map[string]string{
		"Demo Request Tool":        `{"scope":"tool","target":"demo_workshop.add","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false}`,
		"Demo Request Constraints": `{"scope":"tool","target":"demo_workshop.add","constraint":{"version":2,"equals":{"/a":1}},"duration_seconds":null,"future_tools_acknowledged":false}`,
		"Demo Request Duration":    `{"scope":"tool","target":"demo_workshop.add","constraint":null,"duration_seconds":"3600","future_tools_acknowledged":false}`,
		"Demo Request Server":      `{"scope":"server","target":"demo_workshop","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true}`,
		"Demo Request Read-only":   `{"scope":"server","target":"demo_workshop","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true,"read_only":true}`,
	}
	files := map[string]string{
		"Demo Request Tool": "request-tool-bearer", "Demo Request Constraints": "request-constraints-bearer", "Demo Request Duration": "request-duration-bearer", "Demo Request Server": "request-server-bearer", "Demo Request Read-only": "request-read-only-bearer",
	}
	seen := map[string]bool{}
	for _, item := range rows(c.get("grant-requests"), "items") {
		row, _ := item.(map[string]any)
		label := principals[text(row, "principal_id")]
		require.Contains(t, expected, label)
		require.False(t, seen[label])
		seen[label] = true
		policy, err := json.Marshal(value(row, "requested_policy"))
		require.NoError(t, err)
		require.JSONEq(t, expected[label], string(policy), label)
		bearer, err := readBearer(filepath.Join(root, files[label]))
		require.NoError(t, err)
		require.Equal(t, "call_rejected", text(c.call(bearer, "demo_workshop.add", object{"a": 1, "b": 2}), "error", "data", "code"), label)
		path := "/api/v1/grant-requests/" + text(row, "id")
		_, headers := c.request("GET", path, nil, nil, 200, "")
		approved, _ := c.request("POST", path+"/approve", object{"description": label, "approved_policy": value(row, "requested_policy")}, http.Header{"If-Match": {headers.Get("Etag")}}, 200, "")
		require.NoError(t, c.err, label)
		require.Equal(t, "approved", text(approved, "state"))
		grant := c.get("grants/" + text(approved, "approved_grant_id"))
		require.NoError(t, c.err)
		if label == "Demo Request Read-only" {
			require.Equal(t, true, value(grant, "read_only"))
			require.True(t, contentIs(c.call(bearer, "demo_workshop.echo", object{"text": "read-only works"}), "read-only works"))
			require.Equal(t, "call_rejected", text(c.call(bearer, "demo_workshop.add", object{"a": 1, "b": 2}), "error", "data", "code"))
			require.Equal(t, "call_rejected", text(c.call(bearer, "demo_workshop.controlled_error", object{}), "error", "data", "code"))
		} else {
			require.True(t, contentIs(c.call(bearer, "demo_workshop.add", object{"a": 1, "b": 2}), "3"), label)
		}
		if label == "Demo Request Constraints" {
			require.Equal(t, "call_rejected", text(c.call(bearer, "demo_workshop.add", object{"a": 2, "b": 2}), "error", "data", "code"))
		}
		if label == "Demo Request Duration" {
			expiry, err := time.Parse(time.RFC3339Nano, text(grant, "expires_at"))
			require.NoError(t, err)
			require.WithinDuration(t, time.Now().Add(time.Hour), expiry, time.Minute)
		} else {
			require.Nil(t, value(grant, "expires_at"))
		}
		require.NoError(t, c.err, label)
	}
	require.Len(t, seen, 5)
}
