package httppolicy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// This generator has an independent, constructive acceptance oracle. Returning
// early on parser errors would hide the over-rejection this corpus is for.
func checkGeneratedTarget(t *testing.T, seed []byte) {
	t.Helper()
	if len(seed) > 64 {
		seed = seed[:64]
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_~@"
	var plain, escaped strings.Builder
	plain.WriteString("/packages/p")
	escaped.WriteString("/packages/p")
	for _, b := range seed {
		c := alphabet[int(b)%len(alphabet)]
		plain.WriteByte(c)
		if b&1 == 0 && c != '@' {
			fmt.Fprintf(&escaped, "%%%02X", c)
		} else {
			escaped.WriteByte(c)
		}
	}
	plain.WriteString("/item.tgz")
	escaped.WriteString("/item.tgz")
	r, err := ParseRequest("https://api.example.com"+escaped.String()+"?q=%2F+%40&x=1&x=2", "GET", "API.EXAMPLE.COM:443", "api.example.com", nil)
	if err != nil {
		t.Fatalf("known valid target rejected: %q: %v", escaped.String(), err)
	}
	if r.Path() != plain.String() {
		t.Fatalf("resource drift: %q != %q", r.Path(), plain.String())
	}
	assertPolicyForwarding(t, r)
}

func assertPolicyForwarding(t *testing.T, r Request) {
	t.Helper()
	forwarded, err := http.NewRequest(r.Method(), r.URL().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if forwarded.URL.Path != r.Path() || forwarded.Host != r.Destination().Authority() || forwarded.URL.RawQuery != r.URL().RawQuery || forwarded.URL.ForceQuery != r.URL().ForceQuery {
		t.Fatal("forwarding coordinate drift")
	}
	again, err := ParseRequest(forwarded.URL.String(), r.Method(), forwarded.Host, "", nil)
	if err != nil || again != r {
		t.Fatalf("canonical round trip drift: %v", err)
	}
	p := basePolicy(contract.HTTPAllowRequests)
	p.Request.Origin = contract.HTTPOriginSelector{Scheme: r.Scheme(), Host: r.Destination().Host(), Port: r.Destination().Port()}
	p.Request.Path = contract.HTTPPathSelector{Kind: contract.HTTPPathExact, Value: r.Path()}
	e := evaluator(t, snapshot(grant(t, 10, p)))
	decision, err := e.RequestPolicy(again)
	if err != nil || !decision.Allowed {
		t.Fatalf("exact policy did not authorize forwarded resource: %+v %v", decision, err)
	}
	sibling := *forwarded.URL
	sibling.Path += "-sibling"
	sibling.RawPath = ""
	different, err := ParseRequest(sibling.String(), r.Method(), sibling.Host, "", nil)
	if err == nil {
		denied, err := e.RequestPolicy(different)
		if err != nil || denied.Allowed {
			t.Fatal("exact policy widened to sibling")
		}
	}
}

func TestHTTPGeneratedValidCorpus(t *testing.T) {
	for i := 0; i < 256; i++ {
		checkGeneratedTarget(t, []byte{byte(i), byte(255 - i), byte(i / 2)})
	}
}

func FuzzHTTPKnownValidPolicyForwarding(f *testing.F) {
	for _, seed := range [][]byte{[]byte("anthropic-ai/sdk"), {0, 1, 63, 127, 255}, {}} {
		f.Add(seed)
	}
	f.Fuzz(checkGeneratedTarget)
}

func FuzzHTTPPolicyForwarding(f *testing.F) {
	for _, raw := range []string{"https://api.example.com/@scope/pkg", "https://api.example.com/%61pi?", "https://api.example.com/@scope%2fpkg", "https://[2001:4860:4860::8888]/"} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > contract.HTTPTargetBytes+1 {
			return
		}
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		r, err := ParseRequest(raw, "GET", u.Host, "", nil)
		if err != nil {
			return
		}
		assertPolicyForwarding(t, r)
	})
}

func FuzzHTTPConnectAgreement(f *testing.F) {
	f.Add("api.example.com:443", "API.EXAMPLE.COM:443")
	f.Add("[2001:4860:4860::8888]:443", "[2001:4860:4860::8888]:443")
	f.Fuzz(func(t *testing.T, authority, host string) {
		d, err := ParseConnect(authority, host)
		if err != nil {
			return
		}
		again, err := ParseConnect(d.Authority(), d.Authority())
		if err != nil || again != d {
			t.Fatal("CONNECT round trip drift")
		}
		r, err := ParseRequest("https://"+d.Authority()+"/", "GET", d.Authority(), d.Host(), &d)
		if err != nil || r.Destination() != d {
			t.Fatalf("CONNECT/inner request disagreement: %v", err)
		}
	})
}
