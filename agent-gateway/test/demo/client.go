//go:build e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type client struct {
	ctx            context.Context
	listen, bearer string
	sequence       int
	err            error
	http           *http.Client
}

func newClient(ctx context.Context, listen, bearer string) *client {
	return &client{ctx: ctx, listen: listen, bearer: bearer, http: &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *client) request(method, path string, body any, headers http.Header, status int, bearer string) (object, http.Header) {
	if c.err != nil {
		return nil, nil
	}
	var data []byte
	if body != nil {
		data, c.err = json.Marshal(body)
		if c.err != nil {
			c.err = errors.New("invalid public request")
			return nil, nil
		}
	}
	req, err := http.NewRequestWithContext(c.ctx, method, "http://"+c.listen+path, bytes.NewReader(data))
	if err != nil {
		c.err = errors.New("invalid public request")
		return nil, nil
	}
	if bearer == "" {
		bearer = c.bearer
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, values := range headers {
		req.Header[key] = values
	}
	response, err := c.http.Do(req)
	if err != nil {
		c.err = errors.New("public request failed or timed out")
		return nil, nil
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, outputLimit+1))
	if err != nil || len(raw) > outputLimit {
		c.err = errors.New("public response exceeded bound or failed")
		return nil, nil
	}
	if response.StatusCode != status {
		c.err = fmt.Errorf("%s %s returned HTTP %d", method, strings.Split(path, "?")[0], response.StatusCode)
		return nil, nil
	}
	result := object{}
	if len(raw) > 0 && json.Unmarshal(raw, &result) != nil {
		c.err = errors.New("invalid public response")
		return nil, nil
	}
	return result, response.Header
}
func (c *client) get(path string) object {
	result, _ := c.request("GET", "/api/v1/"+path, nil, nil, 200, "")
	return result
}
func (c *client) post(path string, body object) object {
	result, _ := c.request("POST", "/api/v1/"+path, body, nil, 201, "")
	return result
}
func (c *client) rpc(bearer, method string, params object) object {
	c.sequence++
	params["_meta"] = object{"io.modelcontextprotocol/protocolVersion": protocol, "io.modelcontextprotocol/clientInfo": object{"name": "serve-demo", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": object{}}
	result, _ := c.request("POST", "/mcp", object{"jsonrpc": "2.0", "id": c.sequence, "method": method, "params": params}, http.Header{"Accept": {"application/json, text/event-stream"}, "Mcp-Protocol-Version": {protocol}}, 200, bearer)
	return result
}
func (c *client) call(bearer, name string, args object) object {
	return c.rpc(bearer, "tools/call", object{"name": name, "arguments": args})
}
func value(obj object, path ...string) any {
	var result any = obj
	for _, key := range path {
		current, ok := result.(map[string]any)
		if !ok {
			return nil
		}
		result = current[key]
	}
	return result
}
func text(obj object, path ...string) string {
	result, _ := value(obj, path...).(string)
	return result
}
func rows(obj object, path ...string) []any { result, _ := value(obj, path...).([]any); return result }
func (c *client) require(ok bool, message string) {
	if !ok && c.err == nil {
		c.err = errors.New(message)
	}
}
func contentIs(result object, want string) bool {
	list := rows(result, "result", "content")
	if len(list) != 1 {
		return false
	}
	item, ok := list[0].(map[string]any)
	return ok && len(item) == 2 && item["type"] == "text" && item["text"] == want
}
