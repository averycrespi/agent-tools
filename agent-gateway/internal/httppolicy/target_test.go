package httppolicy

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func TestHTTPCanonicalCoordinatesAndForwarding(t *testing.T) {
	for _, tc := range []struct {
		url, host, wantHost, wantPath string
		port                          uint16
	}{
		{"https://API.Example.COM/%61pi/~user?x=%2f", "api.example.com:443", "api.example.com", "/api/~user", 443},
		{"http://example.com", "EXAMPLE.COM", "example.com", "/", 80},
		{"https://bücher.example:8443/item", "xn--bcher-kva.example:8443", "xn--bcher-kva.example", "/item", 8443},
		{"https://[2001:4860:4860:0:0:0:0:8888]/", "[2001:4860:4860::8888]:443", "2001:4860:4860::8888", "/", 443},
		{"https://[::ffff:127.0.0.1]/", "127.0.0.1", "127.0.0.1", "/", 443},
	} {
		r, err := ParseRequest(tc.url, "GET", tc.host, "", nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if r.Destination().Host() != tc.wantHost || r.Destination().Port() != tc.port || r.Path() != tc.wantPath {
			t.Fatal(r)
		}
		forwarded, err := http.NewRequest("GET", r.URL().String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if forwarded.URL.Host != r.Destination().Authority() || forwarded.URL.Path != r.Path() || forwarded.URL.RawPath != "" || forwarded.URL.RawQuery != r.URL().RawQuery {
			t.Fatal("forwarding drift", forwarded.URL, r)
		}
		again, err := ParseRequest(forwarded.URL.String(), "GET", forwarded.Host, "", nil)
		if err != nil || again != r {
			t.Fatal("non-idempotent canonicalization", again, r, err)
		}
	}
}

func TestHTTPRejectAmbiguousTargets(t *testing.T) {
	for _, host := range []string{"127.1", "2130706433", "0177.0.0.1", "0x7f000001", "0x7f.0.0.1", "example.com.", "example..com", "-bad.example", "a_b.example", "*.example.com", "[::1]", "fe80::1%en0", "xn--.com", strings.Repeat("a", 254)} {
		if _, err := NewDestination(host, 443); err == nil {
			t.Fatalf("accepted host %q", host)
		}
	}
	for _, path := range []string{"/a/../secret", "/./x", "/%2e%2e/x", "/%2Fsecret", "/%5csecret", "/%252fsecret", "/a//b", "/%00", "/%7f", "/%zz", "/x;admin", "/x%3fadmin", "/café", "/a\\b", strings.Repeat("/a", contract.HTTPPathBytes)} {
		if _, err := ParseRequest("https://api.example.com"+path, "GET", "api.example.com", "", nil); err == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
	for _, raw := range []string{"https://user@api.example.com/", "https://api.example.com:0443/", "https://api.example.com:/", "https://api.example.com:0/", "https://api.example.com/#fragment", "https://api.example.com/?bad=%gg", "//api.example.com/", "ftp://api.example.com/", "https://api.example.com/\n"} {
		if _, err := ParseRequest(raw, "GET", "api.example.com", "", nil); err == nil {
			t.Fatalf("accepted URI %q", raw)
		}
	}
	for _, tc := range []struct{ host, sni, method string }{{"evil.example", "", "GET"}, {"api.example.com:8443", "", "GET"}, {"api.example.com", "evil.example", "GET"}, {"", "", "GET"}, {"api.example.com", "", "get"}, {"api.example.com", "", "CONNECT"}} {
		if _, err := ParseRequest("https://api.example.com/", tc.method, tc.host, tc.sni, nil); err == nil {
			t.Fatal("accepted disagreement", tc)
		}
	}
	d, err := ParseConnect("api.example.com:443", "API.EXAMPLE.COM:443")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRequest("https://api.example.com/", "GET", "api.example.com", "api.example.com", &d); err != nil {
		t.Fatal(err)
	}
	other, _ := NewDestination("other.example", 443)
	if _, err := ParseRequest("https://api.example.com/", "GET", "api.example.com", "", &other); err == nil {
		t.Fatal("CONNECT disagreement accepted")
	}
	if _, err := ParseRequest("http://api.example.com:443/", "GET", "api.example.com:443", "", &d); err == nil {
		t.Fatal("plain request under CONNECT accepted")
	}
	for _, raw := range []string{"api.example.com", "api.example.com:0443", "api.example.com:443/path", "[::1]"} {
		if _, err := ParseConnect(raw, raw); err == nil {
			t.Fatal("invalid CONNECT", raw)
		}
	}
}

func TestHTTPAddressClasses(t *testing.T) {
	for _, tc := range []struct {
		class  AddressClass
		values []string
	}{
		{AddressPublic, []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888"}},
		{AddressPrivate, []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "::1", "fd12::1", "::ffff:127.0.0.1"}},
		{AddressForbidden, []string{"0.0.0.0", "0.1.2.3", "169.254.169.254", "fd00:ec2::254", "100.100.100.200", "224.0.0.1", "240.0.0.1", "192.0.2.1", "198.18.0.1", "::", "ff02::1", "fe80::1", "fe80::1%eth0", "fec0::1", "64:ff9b::7f00:1", "64:ff9b:1::1", "2002:7f00:1::", "2001::1", "2001:db8::1", "3fff::1", "::ffff:169.254.169.254"}},
	} {
		for _, s := range tc.values {
			if got := ClassifyAddress(netip.MustParseAddr(s)); got != tc.class {
				t.Fatalf("%s: %s", s, got)
			}
		}
	}
	if ClassifyAddress(netip.Addr{}) != AddressForbidden {
		t.Fatal("invalid address")
	}
	d, _ := NewDestination("127.0.0.1", 443)
	if _, err := checkAddresses(d, publicFacts(), true); err == nil {
		t.Fatal("literal and pinned IP disagree")
	}
}

func FuzzHTTPPolicyDecode(f *testing.F) {
	f.Add([]byte(`{"version":1,"type":"allow_tunnel","destination":{"host":"example.com","port":443}}`))
	f.Add([]byte(`{"version":999}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		p, err := DecodePolicy(raw)
		if err == nil && !p.valid {
			t.Fatal("invalid success")
		}
	})
}
func FuzzHTTPCanonicalRoundTrip(f *testing.F) {
	f.Add("https://api.example.com/%61pi?q=1", "api.example.com")
	f.Add("https://api.example.com/%2f", "api.example.com")
	f.Fuzz(func(t *testing.T, raw, host string) {
		r, err := ParseRequest(raw, "GET", host, "", nil)
		if err != nil {
			return
		}
		r2, err := ParseRequest(r.URL().String(), r.Method(), r.Destination().Authority(), "", nil)
		if err != nil || r != r2 {
			t.Fatal("round trip drift", err)
		}
	})
}
