//go:build e2e

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func gitFixtureCounts(ctx context.Context, root, port string) (object, error) {
	raw, err := os.ReadFile(filepath.Join(root, "home", "demo-git-trust.json"))
	if err != nil {
		return nil, err
	}
	var trust struct {
		Certificate string `json:"certificate"`
	}
	if json.Unmarshal(raw, &trust) != nil {
		return nil, errors.New("invalid Git fixture certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(trust.Certificate)) {
		return nil, errors.New("invalid Git fixture certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:"+port+"/counts", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("demo Git fixture counter read failed")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 1025))
	var counts object
	if err != nil || response.StatusCode != http.StatusOK || len(raw) > 1024 || json.Unmarshal(raw, &counts) != nil {
		return nil, errors.New("invalid Git fixture counters")
	}
	return counts, nil
}

func demoGitClient(ctx context.Context, root, proxy, agent, remote string, children *[]*child) ([]byte, int, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, -1, errors.New("native Git is required for the curated demo")
	}
	// The supported proxy URL environment sink is populated inside the isolated
	// child. Neither bearer nor upstream material enters argv or Git config.
	const wrapper = `set -eu
root=$1
proxy=$2
agent=$3
git=$4
remote=$5
export https_proxy="http://agent:$(cat "$root/$agent-bearer")@$proxy"
export GIT_SSL_CAINFO="$root/data/http-ca.pem"
exec "$git" -c http.version=HTTP/1.1 -c protocol.version=0 -c credential.helper= ls-remote "$remote" refs/heads/main
`
	command, err := startChild("Git "+agent+" discovery", []string{"/bin/sh", "-c", wrapper, "demo-git", root, proxy, agent, git, remote}, gitEnvironment(filepath.Join(root, "home")))
	if err != nil {
		return nil, -1, err
	}
	*children = append(*children, command)
	if err = command.finish(ctx, 10*time.Second, false); err != nil {
		return nil, -1, err
	}
	*children = (*children)[:len(*children)-1]
	if command.code < 0 {
		return nil, command.code, errors.New("demo Git request timed out or was interrupted")
	}
	return command.stdout.bytes(), command.code, nil
}

func seedGit(c *client, root, proxy, port string, principals map[string]string, children *[]*child) error {
	origin := "https://127.0.0.1:" + port
	secret, err := readBearer(filepath.Join(root, "git-upstream-secret"))
	if err != nil {
		return err
	}
	credential := c.post("git/credentials", object{"name": "Demo Notes local credential", "origin": origin, "recipe": object{"header": "Authorization", "prefix": "Bearer "}, "secret": secret})
	credentialID := text(credential, "id")
	c.require(credentialID != "" && value(credential, "available") == true, "Git credential unavailable")
	repository := c.post("git/repositories", object{"name": "Demo Notes", "url": origin + "/demo-notes.git", "aliases": []string{}, "credential_id": credentialID})
	repositoryID := text(repository, "id")
	c.require(repositoryID != "", "Git repository missing")
	c.post("git/grants", object{"principal_id": principals["Explorer"], "repository_id": repositoryID, "description": "Demo Explorer may read Demo Notes; Demo Reader has no Git grant", "expires_at": nil, "policy": object{"version": 1, "read": true, "refs": []object{}}})
	_, headers := c.request(http.MethodGet, "/api/v2/git/routing-profile", nil, nil, 200, "")
	if c.err != nil {
		return c.err
	}
	c.request(http.MethodPatch, "/api/v2/git/routing-profile", object{"origins": []string{origin}}, http.Header{"If-Match": {headers.Get("ETag")}}, 200, "")
	p, err := strconv.Atoi(port)
	if err != nil {
		return errors.New("invalid Git fixture port")
	}
	// Private-network permission is independent of Git read/ref authority.
	for _, agent := range []string{"Explorer", "Reader"} {
		c.post("http/grants", object{"principal_id": principals[agent], "description": "Local Demo Notes network permission only; Git grants remain required", "expires_at": nil, "policy": object{"version": 1, "type": "allow_requests", "allow_private": true, "request": object{"origin": object{"scheme": "https", "host": "127.0.0.1", "port": p}, "methods": object{"any": true}, "path": object{"kind": "segment_prefix", "value": "/demo-notes.git/"}}}})
	}
	if c.err != nil {
		return c.err
	}
	for _, agent := range []string{"explorer", "reader"} {
		var before object
		if agent == "reader" {
			before, err = gitFixtureCounts(c.ctx, root, port)
			if err != nil {
				return err
			}
		}
		output, code, err := demoGitClient(c.ctx, root, proxy, agent, origin+"/demo-notes.git", children)
		if err != nil {
			return err
		}
		if agent == "explorer" {
			c.require(code == 0 && strings.HasSuffix(string(output), "\trefs/heads/main\n"), "Git allowed discovery failed")
		} else {
			c.require(code != 0 && len(output) == 0, "Git denied discovery unexpectedly succeeded")
		}
		if c.err != nil {
			return c.err
		}
		after, err := gitFixtureCounts(c.ctx, root, port)
		if err != nil {
			return err
		}
		if agent == "reader" {
			c.require(value(before, "allowed") == value(after, "allowed") && value(before, "rejected") == value(after, "rejected"), "Git denied request reached upstream")
		}
		if c.err != nil {
			return c.err
		}
	}
	counts, err := gitFixtureCounts(c.ctx, root, port)
	if err != nil {
		return err
	}
	c.require(value(counts, "allowed") == float64(1) && value(counts, "rejected") == float64(0), "unexpected Git upstream traffic")
	c.require(value(c.get("git/routing-profile"), "active") == true, "Git routing inactive")
	credential = c.get("git/credentials/" + credentialID)
	c.require(len(rows(credential, "referencing_repositories")) == 1, "Git credential reference missing")
	return waitUntil(c.ctx, *children, "Git traffic verification", func() (bool, error) {
		history := rows(c.get("git/traffic"), "items")
		allowed, denied := false, false
		c.require(len(history) <= 2, "unexpected Git history")
		for _, item := range history {
			row, _ := item.(map[string]any)
			c.require(text(row, "admission", "repository", "id") == repositoryID && text(row, "admission", "operation") == "read_discovery", "Git traffic identity mismatch")
			if value(row, "admission", "allowed") == true {
				allowed = text(row, "admission", "principal", "id") == principals["Explorer"] && value(row, "completion", "transfer_complete") == true && value(row, "completion", "status") == float64(200)
			} else {
				denied = text(row, "admission", "principal", "id") == principals["Reader"] && value(row, "admission", "policy", "grant_count") == float64(0)
			}
		}
		return allowed && denied, c.err
	})
}
