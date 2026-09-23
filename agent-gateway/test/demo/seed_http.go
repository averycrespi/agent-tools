//go:build e2e

package main

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The inert credential demonstrates configuration only. Requests are restricted
// to the owned loopback fixture and never exercise the .invalid destination.
func seedHTTP(c *client, proxy, fixturePort, principal, bearer string, children []*child) error {
	credential := c.post("http/credentials", object{
		"name":     "Inert HTTPS example (never contacted)",
		"boundary": object{"host": "demo.invalid", "port": 443, "allow_wildcard": false},
		"recipe":   object{"header": "Authorization", "prefix": "Bearer "},
		"secret":   "disposable-demo-http-material",
	})
	credentialID := text(credential, "id")
	c.require(credentialID != "" && value(credential, "available") == true, "HTTP credential unavailable")
	requestPolicy := func(scheme, host string, port int, path string) object {
		match := object{"kind": "any"}
		if path != "" {
			match = object{"kind": "exact", "value": path}
		}
		return object{"origin": object{"scheme": scheme, "host": host, "port": port}, "methods": object{"any": true}, "path": match}
	}
	c.post("http/grants", object{"principal_id": principal, "description": "Inert credential example; demo.invalid is never contacted", "expires_at": nil, "policy": object{"version": 1, "type": "allow_requests", "allow_private": false, "credential_id": credentialID, "request": requestPolicy("https", "demo.invalid", 443, "")}})
	port, err := strconv.Atoi(fixturePort)
	if err != nil {
		return errors.New("HTTP fixture port invalid")
	}
	for _, entry := range []struct{ kind, path string }{{"allow_requests", "/http-allowed"}, {"block_requests", "/http-blocked"}} {
		policy := object{"version": 1, "type": entry.kind, "request": requestPolicy("http", "127.0.0.1", port, entry.path)}
		if entry.kind == "allow_requests" {
			policy["allow_private"] = true
		}
		c.post("http/grants", object{"principal_id": principal, "description": "Live local fixture: " + entry.kind, "expires_at": nil, "policy": policy})
	}
	if c.err != nil {
		return c.err
	}
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy}), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	for _, entry := range []struct {
		path   string
		status int
	}{{"/http-allowed", 200}, {"/http-blocked", 403}} {
		req, err := http.NewRequestWithContext(c.ctx, "GET", "http://127.0.0.1:"+fixturePort+entry.path, nil)
		if err != nil {
			return errors.New("HTTP demo request invalid")
		}
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("agent:"+bearer)))
		response, err := client.Do(req)
		if err != nil {
			return errors.New("HTTP demo request failed")
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 8193))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != entry.status {
			return errors.New("HTTP demo outcome mismatch")
		}
	}
	fixture := newClient(c.ctx, "127.0.0.1:"+fixturePort, "")
	counts, _ := fixture.request("GET", "/http-counts", nil, nil, 200, "")
	if fixture.err != nil {
		return fixture.err
	}
	c.require(value(counts, "allowed") == float64(1) && value(counts, "blocked") == float64(0), "HTTP blocked request reached fixture or allowed request missing")
	c.require(len(rows(c.get("http/grants"), "items")) == 3, "HTTP demo grants missing")
	credentials := rows(c.get("http/credentials"), "items")
	c.require(len(credentials) == 1, "HTTP demo credential missing")
	if len(credentials) == 1 {
		row, _ := credentials[0].(map[string]any)
		c.require(value(row, "available") == true && len(rows(row, "referencing_grants")) == 1, "HTTP credential reference missing")
	}
	// Completion persistence can settle after the response; only reads may poll.
	return waitUntil(c.ctx, children, "HTTP traffic verification", func() (bool, error) {
		history := rows(c.get("http/traffic"), "items")
		allowed, blocked := false, false
		for _, item := range history {
			row, _ := item.(map[string]any)
			allowed = allowed || (text(row, "decision") == "allow" && text(row, "outcome") == "succeeded")
			blocked = blocked || (text(row, "decision") == "block" && text(row, "outcome") == "not_dispatched")
		}
		c.require(len(history) <= 2, "unexpected HTTP demo traffic")
		return allowed && blocked, c.err
	})
}
