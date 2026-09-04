package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrantRequestTableSummarizesCompleteApprovedPolicy(t *testing.T) {
	raw := json.RawMessage(`{"version":2,"equals":{"/attempt":1e0},"regex":{"/resource":"item-\\d+"}}`)
	requested := contract.Policy{Scope: contract.PolicyServer, Target: "server"}
	approved := contract.Policy{Scope: contract.PolicyTool, Target: "tool", Constraint: &raw}
	table := grantRequestTable([]contract.GrantRequestSummary{{RequestedPolicy: requested, ApprovedPolicy: &approved}}, nil)
	require.Equal(t, []string{"SCOPE", "TARGET", "CONSTRAINT"}, table.Headers[4:7])
	assert.Equal(t, []string{"tool", "tool", "v2 equals (1), regex (1)"}, table.Rows[0][4:7])
}

func TestCLIGrantRequestApproveInputModes(t *testing.T) {
	const resourceID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	requests := make(chan []byte, 6)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		var outer map[string]json.RawMessage
		if json.Unmarshal(body, &outer) != nil || outer["approved_policy"] == nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		response.Header().Set("Content-Type", contract.MediaTypeJSON)
		response.Header().Set("ETag", contract.GrantRequestETag(resourceID, "2"))
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte(`{"id":"` + resourceID + `","principal_id":"` + resourceID + `","state":"approved","revision":"2","requested_policy":{"scope":"tool","target":"example_tool","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false},"approved_policy":` + string(outer["approved_policy"]) + `,"approved_grant_id":"` + resourceID + `","rejection_reason":null,"created_at":"2026-08-30T00:00:00Z","updated_at":"2026-08-30T00:01:00Z","closed_at":"2026-08-30T00:01:00Z","resolved_server_id":"` + resourceID + `","resolved_upstream_name":"example_tool","submitted_evidence":null,"approved_evidence":null,"current_target":{"scope":"tool","target_state":"available","active_state":null,"durable_state":null,"catalog_revision":null,"fingerprint":null,"descriptor":null}}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	bearerPath := filepath.Join(dir, "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(testAdministratorBearer+"\n"), 0o600))
	etag := contract.GrantRequestETag(resourceID, "1")
	execute := func(args ...string) ([]byte, error) {
		command := newRootCmd()
		var stdout, stderr bytes.Buffer
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		command.SetIn(bytes.NewReader(nil))
		base := []string{"--admin-bearer-file", bearerPath, "--address", server.URL, "--output", "json"}
		command.SetArgs(append(args, base...))
		err := command.ExecuteContext(context.Background())
		if err != nil {
			return stderr.Bytes(), err
		}
		return stdout.Bytes(), nil
	}
	approveArgs := []string{"grant-request", "approve", resourceID, "--etag", etag}
	directTool := append(append([]string(nil), approveArgs...), "--description", "Approved access", "--scope", "tool", "--target", "example_tool", "--yes")
	toolBody := `{"description":"Approved access","approved_policy":{"scope":"tool","target":"example_tool","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false}}`

	output, err := execute(directTool...)
	require.NoError(t, err, "%s", output)
	directBody := <-requests
	assert.JSONEq(t, toolBody, string(directBody))

	toolFile := filepath.Join(dir, "tool.json")
	require.NoError(t, os.WriteFile(toolFile, []byte(toolBody), 0o600))
	output, err = execute(append(approveArgs, "--file", toolFile, "--yes")...)
	require.NoError(t, err, "%s", output)
	assert.Equal(t, string(directBody), string(<-requests))

	output, err = execute(append(approveArgs, "--description", "Approved access", "--scope", "tool", "--target", "example_tool", "--duration-seconds", "600", "--yes")...)
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, `{"description":"Approved access","approved_policy":{"scope":"tool","target":"example_tool","constraint":null,"duration_seconds":"600","future_tools_acknowledged":false}}`, string(<-requests))

	output, err = execute(append(approveArgs, "--description", "Approved access", "--scope", "server", "--target", resourceID, "--acknowledge-future-tools", "--yes")...)
	require.NoError(t, err, "%s", output)
	assert.JSONEq(t, `{"description":"Approved access","approved_policy":{"scope":"server","target":"`+resourceID+`","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true}}`, string(<-requests))

	constrainedBody := `{"description":"Approved access","approved_policy":{"scope":"tool","target":"example_tool","constraint":{"version":2,"equals":{"/attempt":1e0},"regex":{"/resource":"item-\\d+"}},"duration_seconds":"900","future_tools_acknowledged":false}}`
	constrainedFile := filepath.Join(dir, "constrained.json")
	require.NoError(t, os.WriteFile(constrainedFile, []byte(constrainedBody), 0o600))
	output, err = execute(append(approveArgs, "--file", constrainedFile, "--yes")...)
	require.NoError(t, err, "%s", output)
	constrainedRequest := string(<-requests)
	assert.JSONEq(t, constrainedBody, constrainedRequest)
	assert.Contains(t, constrainedRequest, `1e0`)
	assert.Contains(t, constrainedRequest, `"item-\\d+"`)

	incompleteFile := filepath.Join(dir, "incomplete.json")
	require.NoError(t, os.WriteFile(incompleteFile, []byte(`{"approved_policy":{"scope":"tool"}}`), 0o600))
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing scope", args: append(approveArgs, "--description", "Approved access", "--target", "example_tool", "--yes")},
		{name: "invalid scope", args: append(approveArgs, "--description", "Approved access", "--scope", "global", "--target", "example_tool", "--yes")},
		{name: "empty target", args: append(approveArgs, "--description", "Approved access", "--scope", "tool", "--target", "", "--yes")},
		{name: "noncanonical duration", args: append(approveArgs, "--description", "Approved access", "--scope", "tool", "--target", "example_tool", "--duration-seconds", "060", "--yes")},
		{name: "short duration", args: append(approveArgs, "--description", "Approved access", "--scope", "tool", "--target", "example_tool", "--duration-seconds", "59", "--yes")},
		{name: "server missing acknowledgement", args: append(approveArgs, "--description", "Approved access", "--scope", "server", "--target", resourceID, "--yes")},
		{name: "tool with acknowledgement", args: append(approveArgs, "--description", "Approved access", "--scope", "tool", "--target", "example_tool", "--acknowledge-future-tools", "--yes")},
		{name: "mixed", args: append(approveArgs, "--description", "Approved access", "--scope", "tool", "--target", "example_tool", "--file", toolFile, "--yes")},
		{name: "incomplete file", args: append(approveArgs, "--file", incompleteFile, "--yes")},
		{name: "unconfirmed", args: append(approveArgs, "--description", "Approved access", "--scope", "tool", "--target", "example_tool")},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := execute(test.args...)
			require.Error(t, err)
			assert.Equal(t, 2, commandExitCode(err), "%s", output)
			assert.Len(t, requests, 0)
		})
	}

	root := newRootCmd()
	command, _, err := root.Find([]string{"grant-request", "approve"})
	require.NoError(t, err)
	for _, flag := range []string{"description", "scope", "target", "duration-seconds", "acknowledge-future-tools", "file"} {
		assert.NotNil(t, command.Flags().Lookup(flag), flag)
	}
	assert.Contains(t, command.Example, "--scope")
	assert.Contains(t, command.Example, "--file")
}
