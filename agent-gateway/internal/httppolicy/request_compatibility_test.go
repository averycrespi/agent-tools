package httppolicy

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func TestRequestWireAndComparison(t *testing.T) {
	for _, tc := range []struct{ input, wire, comparison string }{
		{"/https://example.com", "/https://example.com", "/https://example.com"},
		{"//example.com/x", "//example.com/x", "//example.com/x"},
		{"/@scope%2fpkg", "/@scope%2fpkg", "/@scope%2Fpkg"},
		{"/%40scope/pkg", "/%40scope/pkg", "/%40scope/pkg"},
		{"/a/b", "/a/b", "/a/b"}, {"/a%2fb", "/a%2fb", "/a%2Fb"},
		{"/a%252Fb", "/a%252Fb", "/a%252Fb"}, {"/a//b", "/a//b", "/a//b"},
		{"/%61pi?x=1&x=2+3&escaped=%00%5c%ff", "/%61pi?x=1&x=2+3&escaped=%00%5c%ff", "/api"},
		{"/!$&'()*+,;=:@", "/!$&'()*+,;=:@", "/!$&'()*+,;=:@"},
		{"/%3a%3B%25%20", "/%3a%3B%25%20", "/%3A%3B%25%20"},
		{"/caf%C3%a9", "/caf%C3%a9", "/caf%C3%A9"},
		{"/café?q=é+é", "/caf%C3%A9?q=%C3%A9+e%CC%81", "/caf%C3%A9"},
		{"/café", "/cafe%CC%81", "/cafe%CC%81"},
		{"/é/%2f/%61", "/%C3%A9/%2f/%61", "/%C3%A9/%2F/a"},
		{"/%252e%252e/%255c", "/%252e%252e/%255c", "/%252e%252e/%255c"},
		{"", "/", "/"}, {"?", "/?", "/"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			r, err := ParseRequest("https://api.example.com"+tc.input, "GET", "api.example.com", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if r.URL().RequestURI() != tc.wire || r.Path() != tc.comparison || r.Destination().Host() != "api.example.com" {
				t.Fatalf("wire=%q comparison=%q", r.URL().RequestURI(), r.Path())
			}
			again, err := ParseRequest(r.URL().String(), "GET", r.Destination().Authority(), "", nil)
			if err != nil || r != again {
				t.Fatalf("admission reparse drift: %v", err)
			}
		})
	}
}

func TestRequestCompatibilityDoesNotExpandSelectors(t *testing.T) {
	for _, path := range []string{"/a:b", "/a//b", "/a%2Fb", "/a%252Fb", "/%40scope/pkg", "/caf%C3%A9"} {
		if _, err := ParseRequest("https://api.example.com"+path, "GET", "api.example.com", "", nil); err != nil {
			t.Fatal(err)
		}
		p := basePolicy(contract.HTTPAllowRequests)
		p.Request.Path = contract.HTTPPathSelector{Kind: contract.HTTPPathExact, Value: path}
		if _, err := Compile(p); err == nil {
			t.Fatalf("selector grammar expanded: %q", path)
		}
	}
}

func TestRequestPrefixCompatibilityMatrix(t *testing.T) {
	for _, kind := range []contract.HTTPGrantType{contract.HTTPAllowRequests, contract.HTTPBlockRequests} {
		for _, selector := range []contract.HTTPPathSelector{{Kind: contract.HTTPPathExact, Value: "/a"}, {Kind: contract.HTTPPathPrefix, Value: "/a"}, {Kind: contract.HTTPPathPrefix, Value: "/"}, {Kind: contract.HTTPPathAny}} {
			p := basePolicy(kind)
			p.Request.Path = selector
			s := snapshot(grant(t, 10, p))
			if kind == contract.HTTPBlockRequests {
				s.Default = contract.HTTPDefaultAllow
			}
			e := evaluator(t, s)
			for _, path := range []string{"/a", "/%61", "/a/b", "/ab", "/a//b", "/a%2Fb", "/a/%2Fb", "/a%252Fb"} {
				r := request(t, path)
				matches := selector.Kind == contract.HTTPPathAny || selector.Value == "/" || r.Path() == "/a" || selector.Kind == contract.HTTPPathPrefix && strings.HasPrefix(r.Path(), "/a/")
				want := matches
				if kind == contract.HTTPBlockRequests {
					want = !matches
				}
				d, err := e.RequestPolicy(r)
				if err != nil || d.Allowed != want {
					t.Fatalf("%s %+v %s: %+v %v", kind, selector, path, d, err)
				}
			}
		}
	}
}

func TestNewTargetsRetainCredentialAndPrivateBoundaries(t *testing.T) {
	p := basePolicy(contract.HTTPAllowRequests)
	p.Request.Path = contract.HTTPPathSelector{Kind: contract.HTTPPathPrefix, Value: "/a"}
	p.AllowPrivate = boolp(true)
	p.CredentialID = stringp(id(80))
	s := snapshot(grant(t, 10, p))
	s.Default = contract.HTTPDefaultAllow
	s.Credentials = []Credential{credential(80, "api.example.com", 443), credential(81, "api.example.com", 443)}
	facts := AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	for _, path := range []string{"/a//b", "/a/%2fb", "/a/%252Fb", "/%61/https://example.com"} {
		d, err := evaluator(t, s).Request(request(t, path), facts)
		if err != nil || !d.Allowed || d.Credential == nil || d.Credential.ID != id(80) {
			t.Fatalf("matching grant: %+v %v", d, err)
		}
		unavailable := s
		unavailable.Credentials = []Credential{credential(80, "api.example.com", 443), credential(81, "api.example.com", 443)}
		unavailable.Credentials[0].Available = false
		d, err = evaluator(t, unavailable).Request(request(t, path), facts)
		if err != nil || d.Allowed || d.Reason != contract.HTTPReasonCredentialUnavailable {
			t.Fatalf("unavailable: %+v %v", d, err)
		}
		conflict := s
		p.CredentialID = stringp(id(81))
		conflict.Grants = append([]Grant{s.Grants[0]}, grant(t, 11, p))
		d, err = evaluator(t, conflict).Request(request(t, path), facts)
		if err != nil || d.Allowed || d.Reason != contract.HTTPReasonCredentialConflict {
			t.Fatalf("conflict: %+v %v", d, err)
		}
	}
	for _, raw := range []string{"https://api.example.com/a%2Fb", "https://api.example.com/a%252Fb", "https://other.example.com/a//b", "https://api.example.com:8443/a//b"} {
		// The default allows policy eligibility, but a nonmatching grant supplies
		// neither private permission nor a credential, even for accepted new syntax.
		host := strings.Split(strings.TrimPrefix(raw, "https://"), "/")[0]
		r, err := ParseRequest(raw, "GET", host, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		d, err := evaluator(t, s).Request(r, facts)
		if err != nil || d.Allowed || d.Credential != nil || d.PrivateGrant != nil {
			t.Fatalf("nonmatching scope: %+v %v", d, err)
		}
	}
}

func TestRequestEncodingBoundsAndReasons(t *testing.T) {
	for _, size := range []int{contract.HTTPPathBytes - 1, contract.HTTPPathBytes, contract.HTTPPathBytes + 1} {
		// UTF-8 é consumes six escaped bytes, versus two input bytes.
		path := "/" + strings.Repeat("é", (size-1)/6) + strings.Repeat("a", (size-1)%6)
		_, err := ParseRequest("https://api.example.com"+path, "GET", "api.example.com", "", nil)
		if (err == nil) != (size <= contract.HTTPPathBytes) {
			t.Fatalf("escaped bound %d: %v", size, err)
		}
	}
	d, _ := ParseConnect("api.example.com:443", "api.example.com:443")
	for _, size := range []int{contract.HTTPTargetBytes - 1, contract.HTTPTargetBytes, contract.HTTPTargetBytes + 1} {
		prefix := "https://" + d.Authority() + "/?"
		raw := prefix + strings.Repeat("a", size-len(prefix))
		_, err := ParseRequest(raw, "GET", d.Authority(), d.Host(), &d)
		if (err == nil) != (size <= contract.HTTPTargetBytes) {
			t.Fatalf("CONNECT expansion bound %d: %v", size, err)
		}
	}
	for _, size := range []int{contract.HTTPTargetBytes - 1, contract.HTTPTargetBytes, contract.HTTPTargetBytes + 1} {
		prefix := "https://api.example.com:443/?"
		remaining := size - len(prefix)
		raw := prefix + strings.Repeat("é", remaining/6) + strings.Repeat("a", remaining%6)
		_, err := ParseRequest(raw, "GET", "api.example.com", "", nil)
		if (err == nil) != (size <= contract.HTTPTargetBytes) {
			t.Fatalf("Unicode target expansion %d: %v", size, err)
		}
	}
	for _, tc := range []struct{ path, reason string }{
		{"/%xx", "invalid_target_syntax"}, {"/a b", "invalid_target_syntax"}, {"/a\x00b", "invalid_target_syntax"}, {"/\xff", "invalid_target_syntax"},
		{"/%00", "forbidden_path"}, {"/%7f", "forbidden_path"}, {"/%5c", "forbidden_path"}, {"/a/%2e%2E", "forbidden_path"},
		{"/" + strings.Repeat("a", contract.HTTPPathBytes), "target_too_long"},
	} {
		_, err := ParseRequest("https://api.example.com"+tc.path, "GET", "api.example.com", "", nil)
		if err == nil || RejectionReason(err) != tc.reason {
			t.Fatalf("unexpected reason: %v", err)
		}
	}
}
