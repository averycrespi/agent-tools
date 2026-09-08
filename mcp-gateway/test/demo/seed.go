//go:build e2e

package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
)

func seed(ctx context.Context, c *client, root string, endpoints map[string]string, children []*child, command func(string, []string) error) error {
	servers := map[string]string{}
	for _, kind := range []string{"workshop", "library"} {
		label := map[string]string{"workshop": "Workshop", "library": "Library"}[kind]
		body := object{"namespace": "demo_" + kind, "display_name": "Demo " + label, "enabled": true, "transport": object{"kind": "streamable_http", "url": "http://127.0.0.1:" + endpoints[kind] + "/mcp", "protocol_mode": "modern", "authentication": object{"mode": "none"}}}
		result, _ := c.request("POST", "/api/v1/servers", body, http.Header{"Idempotency-Key": {"demo-" + kind}}, 201, "")
		id := text(result, "server", "id")
		c.require(id != "", "server creation failed")
		if c.err != nil {
			return c.err
		}
		servers[kind] = id
		err := waitUntil(ctx, children, "fixture catalog", func() (bool, error) {
			server := c.get("servers/" + id)
			return text(server, "runtime", "state") == "active" && text(server, "catalog", "active_state") == "current" && value(server, "catalog", "active_tool_count") == float64(len(tools[kind])), c.err
		})
		if err != nil {
			return err
		}
	}
	principals := map[string]string{}
	agents := map[string]string{}
	for _, label := range []string{"Explorer", "Reader", "Disabled"} {
		visibility := "allowed-only"
		if label == "Reader" {
			visibility = "requestable"
		}
		result := c.post("principals", object{"display_name": "Demo " + label, "visibility": visibility})
		id := text(result, "principal", "id")
		c.require(id != "", "principal creation failed")
		if c.err != nil {
			return c.err
		}
		principals[label] = id
	}
	for _, label := range []string{"Explorer", "Reader", "Disabled"} {
		sink := filepath.Join(root, strings.ToLower(label)+"-bearer")
		err := command("issue "+label, []string{"principal", "credential", "issue", principals[label], "--secret-output", sink, "--yes", "--address", "http://" + c.listen, "--admin-bearer-file", filepath.Join(root, "admin-bearer")})
		if err != nil {
			return err
		}
		bearer, err := readBearer(sink)
		if err != nil {
			return err
		}
		agents[label] = bearer
	}
	current, headers := c.request("GET", "/api/v1/principals/"+principals["Disabled"], nil, nil, 200, "")
	c.request("PATCH", "/api/v1/principals/"+text(current, "id"), object{"state": "disabled"}, http.Header{"If-Match": {headers.Get("Etag")}}, 200, "")
	for _, grant := range []struct{ label, kind, name string }{{"Explorer", "workshop", ""}, {"Explorer", "library", ""}, {"Reader", "workshop", "echo"}, {"Reader", "library", "lookup"}, {"Reader", "workshop", "controlled_error"}} {
		effect := "allow"
		if grant.name == "controlled_error" {
			effect = "deny"
		}
		var name any
		if grant.name != "" {
			name = grant.name
		}
		c.post("grants", object{"description": "Demo " + grant.label + " " + grant.kind + " access", "principal_id": principals[grant.label], "effect": effect, "server_id": servers[grant.kind], "upstream_name": name, "constraint": nil, "expires_at": nil})
	}
	for _, call := range []struct {
		name string
		args object
		want string
	}{{"demo_workshop.echo", object{"text": "Hello from the demo"}, "Hello from the demo"}, {"demo_workshop.add", object{"a": 19, "b": 23}, "42"}, {"demo_library.lookup", object{"document": "welcome"}, documents["welcome"]}} {
		c.require(contentIs(c.call(agents["Explorer"], call.name, call.args), call.want), "seed invocation result mismatch")
	}
	c.require(text(c.call(agents["Explorer"], "demo_workshop.controlled_error", object{}), "error", "data", "code") == "downstream_failure", "controlled error missing")
	c.require(contentIs(c.call(agents["Reader"], "demo_workshop.echo", object{"text": "Reader access works"}), "Reader access works"), "restricted allow failed")
	c.require(text(c.call(agents["Reader"], "demo_workshop.add", object{"a": 1, "b": 2}), "error", "data", "code") == "call_rejected", "restricted call was not denied")
	c.request("POST", "/mcp", object{}, http.Header{"Accept": {"application/json, text/event-stream"}, "Mcp-Protocol-Version": {protocol}}, 401, agents["Disabled"])
	result := c.call(agents["Reader"], "mcp_gateway.create_grant_request", object{"policy": object{"scope": "tool", "target": "demo_workshop.add", "constraint": nil, "duration_seconds": nil, "future_tools_acknowledged": false}})
	c.require(value(result, "error") == nil && value(result, "result", "isError") != true, "grant request failed")
	for _, collection := range []struct {
		name  string
		count int
	}{{"servers", 2}, {"principals", 3}, {"grants", 8}} {
		c.require(len(rows(c.get(collection.name), "items")) == collection.count, collection.name+" verification failed")
	}
	requests := rows(c.get("grant-requests"), "items")
	pending := false
	if len(requests) == 1 {
		row, _ := requests[0].(map[string]any)
		pending = text(row, "state") == "pending"
	}
	c.require(pending, "pending request missing")
	success, failure := false, false
	for _, item := range rows(c.get("invocations"), "items") {
		row, _ := item.(map[string]any)
		if text(row, "requested_name") == "demo_workshop.add" && text(row, "outcome", "class") == "succeeded" {
			success = true
		}
		if text(row, "requested_name") == "demo_workshop.controlled_error" && text(row, "outcome", "class") == "downstream_failure" {
			failure = true
		}
	}
	c.require(success && failure, "invocation history missing")
	for _, label := range []string{"Explorer", "Reader"} {
		names := map[string]bool{}
		for _, item := range rows(c.rpc(agents[label], "tools/list", object{}), "result", "tools") {
			row, _ := item.(map[string]any)
			names[text(row, "name")] = true
		}
		c.require(names["demo_workshop.echo"] && names["demo_workshop.add"] && names["demo_library.lookup"] && names["demo_workshop.controlled_error"] == (label == "Explorer"), "demo discovery mismatch")
	}
	return c.err
}
