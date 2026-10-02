package gitwire

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

func target(t *testing.T, path, method string) httppolicy.Request {
	t.Helper()
	r, err := httppolicy.ParseRequest("https://github.com/acme/repo"+path, method, "github.com", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestRoute(t *testing.T) {
	for _, tc := range []struct {
		path, method, operation string
		shaped, valid           bool
	}{
		{"/info/refs?service=git-upload-pack", "GET", "read_discovery", true, true},
		{"/info/refs?service=git-receive-pack", "GET", "push_discovery", true, true},
		{"/git-receive-pack", "POST", "push", true, true},
		{"/git-upload-pack", "POST", "read", true, true},
		{"/releases/download/v1/app.zip", "GET", "", false, true},
		{"/archive/main.zip", "GET", "", false, true},
		{"/releases/download/v1/HEAD", "GET", "", false, true},
		{"/releases/download/objects/app.zip", "GET", "", false, true},
		{"/releases/download/v1/git-receive-pack.exe", "GET", "", false, true},
		{"/releases/download/v1/git-receive-pack", "GET", "", false, true},
		{"/issues?q=service", "GET", "", false, true},
		{"?q=git-upload-pack&service=search", "GET", "", false, true},
		{"/git-receive-pack", "GET", "", true, false},
		{"/git-receive-pack?x=1", "POST", "", true, false},
		{"/info/refs?service=git-upload-pack&service=git-receive-pack", "GET", "", true, false},
		{"/info/refs?service=git-unknown", "GET", "", true, false},
		{"/info/refs?%73ervice=git-upload-pack", "GET", "", true, false},
		{"/git%2dreceive-pack", "POST", "", true, false},
		{"/HEAD", "GET", "", true, false},
		{"/objects/info/packs", "GET", "", true, false},
	} {
		t.Run(tc.path+tc.method, func(t *testing.T) {
			_, operation, shaped, err := Route(target(t, tc.path, tc.method))
			if shaped != tc.shaped || (err == nil) != tc.valid || tc.valid && operation != tc.operation {
				t.Fatalf("operation=%s shaped=%v error=%v", operation, shaped, err)
			}
		})
	}
}
func pkt(value string) string { return fmt.Sprintf("%04x%s", len(value)+4, value) }
func push(t *testing.T, body string) (*Request, error) {
	t.Helper()
	return New(context.Background(), target(t, "/git-receive-pack", "POST"), contract.GitRepository{ID: "repo", GitRepositoryDefinition: contract.GitRepositoryDefinition{URL: "https://github.com:443/acme/repo", Aliases: []string{}}, Revision: "1", AliasRevision: "1"}, "1", http.Header{"Content-Type": {"application/x-git-receive-pack-request"}}, io.NopCloser(strings.NewReader(body)))
}
func TestReceiveControls(t *testing.T) {
	zero := strings.Repeat("0", 40)
	one := strings.Repeat("1", 40)
	create := zero + " " + one + " refs/heads/main"
	remove := one + " " + zero + " refs/heads/main"
	for _, tc := range []struct {
		name, body, operation string
		valid                 bool
	}{
		{"probe", "0000", "probe", true},
		{"probe trailing", "0000x", "", false},
		{"create", pkt(create+"\x00report-status-v2 atomic object-format=sha1") + "0000PACKopaque", "push", true},
		{"native capability separator", pkt(create+"\x00 report-status-v2 side-band-64k quiet object-format=sha1 agent=git/2.43.0") + "0000PACKopaque", "push", true},
		{"delete", pkt(remove) + "0000", "push", true},
		{"delete trailing", pkt(remove) + "0000PACK", "", false},
		{"duplicate", pkt(create) + pkt(create) + "0000PACK", "", false},
		{"push options", pkt(create+"\x00push-options") + "0000PACK", "", false},
		{"sha256", pkt(create+"\x00object-format=sha256") + "0000PACK", "", false},
		{"certificate", pkt("push-cert") + "0000PACK", "", false},
		{"invalid oid", pkt("x"+create[1:]) + "0000PACK", "", false},
		{"missing pack", pkt(create) + "0000", "", false},
		{"invalid ref", pkt(zero+" "+one+" refs/heads/../bad") + "0000PACK", "", false},
		{"delimiter", "0001", "", false},
		{"nul later", pkt(create) + pkt(remove+"\x00atomic") + "0000PACK", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := push(t, tc.body)
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v", err)
			}
			if err != nil {
				return
			}
			if r.Operation() != tc.operation {
				t.Fatal(r.Operation())
			}
			body, err := r.Dispatch()
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(body)
			if err != nil || string(got) != tc.body {
				t.Fatal("wire changed", err)
			}
			if _, err := r.Dispatch(); err == nil {
				t.Fatal("second dispatch allowed")
			}
		})
	}
}
func TestRequestCopiesCannotReplacePrefix(t *testing.T) {
	zero := strings.Repeat("0", 40)
	one := strings.Repeat("1", 40)
	original := pkt(zero+" "+one+" refs/heads/main") + "0000PACKopaque"
	r, err := push(t, original)
	if err != nil {
		t.Fatal(err)
	}
	actions := r.Actions()
	actions[0].Ref = "refs/heads/other"
	repo := r.Repository()
	repo.URL = "https://attacker.example/repo"
	body, err := r.Dispatch()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	if err != nil || string(got) != original || r.Actions()[0].Ref != "refs/heads/main" {
		t.Fatal("mutable request binding")
	}
}
