package httppolicy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func TestHTTPScopedTarballCompatibility(t *testing.T) {
	for _, path := range []string{"/@anthropic-ai/sdk/-/sdk-0.124.0.tgz", "/word-wrap/-/word-wrap-1.2.5.tgz"} {
		t.Run(path, func(t *testing.T) {
			r, err := ParseRequest("https://registry.npmjs.org"+path, "GET", "registry.npmjs.org", "registry.npmjs.org", nil)
			if err != nil {
				t.Fatal(err)
			}
			want := path
			if r.Path() != want {
				t.Fatalf("path %q, want %q", r.Path(), want)
			}
			forwarded, err := http.NewRequest("GET", r.URL().String(), nil)
			if err != nil || forwarded.URL.RequestURI() != want {
				t.Fatalf("forwarded target: %v %v", forwarded, err)
			}
		})
	}
}

func TestHTTPCompatibilityCorpus(t *testing.T) {
	for _, tc := range []struct{ raw, host, wantHost, path, query string }{
		{"http://example.com", "EXAMPLE.COM:80", "example.com", "/", ""},
		{"https://example.com?", "example.com", "example.com", "/", ""},
		{"https://bücher.example/@scope/pkg", "xn--bcher-kva.example:443", "xn--bcher-kva.example", "/@scope/pkg", ""},
		{"https://[2001:4860:4860::8888]:8443/%61pi", "[2001:4860:4860::8888]:8443", "2001:4860:4860::8888", "/api", ""},
		{"https://[::ffff:127.0.0.1]/@pkg", "127.0.0.1:443", "127.0.0.1", "/@pkg", ""},
		{"https://example.com:65535/a.b-~_9/?q=%2f+%40&x=1&x=2", "example.com:65535", "example.com", "/a.b-~_9/", "q=%2f+%40&x=1&x=2"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			r, err := ParseRequest(tc.raw, "GET", tc.host, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if r.Destination().Host() != tc.wantHost || r.Path() != tc.path || r.URL().RawQuery != tc.query {
				t.Fatalf("unexpected canonical coordinates: %+v", r)
			}
			assertPolicyForwarding(t, r)
		})
	}
}

func TestHTTPScopedPrefixPolicy(t *testing.T) {
	p := basePolicy(contract.HTTPAllowRequests)
	p.Request.Path = contract.HTTPPathSelector{Kind: contract.HTTPPathPrefix, Value: "/@scope"}
	e := evaluator(t, snapshot(grant(t, 10, p)))
	for _, tc := range []struct {
		path    string
		allowed bool
	}{{"/@scope/pkg", true}, {"/@scope", true}, {"/@scope-sibling/pkg", false}, {"/other/@scope/pkg", false}} {
		r := request(t, tc.path)
		d, err := e.RequestPolicy(r)
		if err != nil || d.Allowed != tc.allowed {
			t.Fatalf("prefix %q: %+v %v", tc.path, d, err)
		}
	}
}

func TestHTTPCompatibilityLimits(t *testing.T) {
	for _, path := range []string{"/%40scope/pkg", "/@scope%2fpkg", "/@scope%2Fpkg", "/%2540scope/pkg", "/@scope/%2e%2e/pkg", "/@scope//pkg", "/@scope/%00", "/@scope/%5cpkg", "/a:b", "/a;b", "/a+b", "/a%3fb", "/café"} {
		if _, err := ParseRequest("https://registry.npmjs.org"+path, "GET", "registry.npmjs.org", "", nil); err == nil {
			t.Fatalf("accepted deliberately unsupported path %q", path)
		}
	}
	for _, size := range []int{contract.HTTPPathBytes, contract.HTTPPathBytes + 1} {
		_, err := ParseRequest("https://registry.npmjs.org/"+strings.Repeat("a", size-1), "GET", "registry.npmjs.org", "", nil)
		if (err == nil) != (size == contract.HTTPPathBytes) {
			t.Fatalf("path bound %d: %v", size, err)
		}
	}
}
