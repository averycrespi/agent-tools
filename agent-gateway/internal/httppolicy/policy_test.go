package httppolicy

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func id(n int) string { return fmt.Sprintf("%026d", n) }
func ref(n int) contract.HTTPRevisionRef {
	return contract.HTTPRevisionRef{ID: id(n), Revision: uint64(n + 1)}
}
func basePolicy(kind contract.HTTPGrantType) contract.HTTPPolicy {
	p := contract.HTTPPolicy{Version: 1, Type: kind}
	if kind == contract.HTTPBlockDestination || kind == contract.HTTPAllowTunnel {
		p.Destination = &contract.HTTPDestinationSelector{Host: "api.example.com", Port: 443}
	} else {
		p.Request = &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: "https", Host: "api.example.com", Port: 443}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}
	}
	return p
}
func grant(t *testing.T, n int, p contract.HTTPPolicy) Grant {
	t.Helper()
	c, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	return Grant{Ref: ref(n), PrincipalID: id(1), Policy: c}
}
func snapshot(grants ...Grant) Snapshot {
	return Snapshot{Principal: ref(1), PolicyRevision: 99, DefaultRevision: 3, Default: contract.HTTPDefaultBlock, Grants: grants}
}
func evaluator(t *testing.T, s Snapshot) *Evaluator {
	t.Helper()
	e, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func request(t *testing.T, path string) Request {
	t.Helper()
	r, err := ParseRequest("https://api.example.com"+path, "GET", "api.example.com", "api.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func publicFacts() AddressFacts {
	return AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
}
func credential(n int, host string, port uint16) Credential {
	return Credential{Ref: ref(n), Available: true, Origins: []contract.HTTPOriginSelector{{Scheme: "https", Host: host, Port: port}}}
}
func boolp(v bool) *bool       { return &v }
func stringp(v string) *string { return &v }

func TestHTTPPrecedenceAndOrderIndependence(t *testing.T) {
	r := request(t, "/write")
	for mask := 0; mask < 16; mask++ {
		for _, def := range []contract.HTTPDefault{contract.HTTPDefaultBlock, contract.HTTPDefaultAllow} {
			var gs []Grant
			for i, k := range contract.HTTPGrantTypes() {
				if mask&(1<<i) != 0 {
					gs = append(gs, grant(t, 10+i, basePolicy(k)))
				}
			}
			s := snapshot(gs...)
			s.Default = def
			e := evaluator(t, s)
			got, err := e.Request(r, publicFacts())
			if err != nil {
				t.Fatal(err)
			}
			wantAllow := mask&1 == 0 && mask&4 == 0 && (mask&8 != 0 || def == contract.HTTPDefaultAllow)
			if got.Allowed != wantAllow {
				t.Fatalf("mask %d default %s: %+v", mask, def, got)
			}
			con, err := e.Connect(r.destination, publicFacts())
			if err != nil {
				t.Fatal(err)
			}
			wantTransport := contract.HTTPTransportIntercept
			if mask&1 != 0 {
				wantTransport = contract.HTTPTransportNone
			} else if mask&2 != 0 {
				wantTransport = contract.HTTPTransportTunnel
			}
			if con.Transport != wantTransport || con.Allowed != (wantTransport == contract.HTTPTransportTunnel) {
				t.Fatalf("connect mask %d: %+v", mask, con)
			}
			rng := rand.New(rand.NewSource(int64(mask)))
			for range 12 {
				rng.Shuffle(len(s.Grants), func(i, j int) { s.Grants[i], s.Grants[j] = s.Grants[j], s.Grants[i] })
				other := evaluator(t, s)
				a, _ := other.Request(r, publicFacts())
				b, _ := other.Connect(r.destination, publicFacts())
				if !reflect.DeepEqual(a, got) || !reflect.DeepEqual(b, con) {
					t.Fatal("input order changed decision/evidence")
				}
			}
		}
	}
}

func TestHTTPPathsMethodsAndPlainHTTP(t *testing.T) {
	tests := []struct {
		kind        contract.HTTPPathKind
		scope, path string
		want        bool
	}{
		{contract.HTTPPathExact, "/api", "/api", true}, {contract.HTTPPathExact, "/api", "/api/", false},
		{contract.HTTPPathPrefix, "/api", "/api", true}, {contract.HTTPPathPrefix, "/api/", "/api/item", true},
		{contract.HTTPPathPrefix, "/api", "/apix", false}, {contract.HTTPPathPrefix, "/", "/anything", true},
		{contract.HTTPPathExact, "/%61pi", "/api", true}, {contract.HTTPPathAny, "", "/write", true},
	}
	for _, tc := range tests {
		p := basePolicy(contract.HTTPAllowRequests)
		p.Request.Path = contract.HTTPPathSelector{Kind: tc.kind, Value: tc.scope}
		p.Request.Methods = contract.HTTPMethods{Values: []string{"GET", "DELETE"}}
		e := evaluator(t, snapshot(grant(t, 10, p)))
		got, err := e.Request(request(t, tc.path), publicFacts())
		if err != nil || got.Allowed != tc.want {
			t.Fatalf("%+v: %+v %v", tc, got, err)
		}
		r := request(t, tc.path)
		r.method = "POST"
		got, err = e.Request(r, publicFacts())
		if err != nil || got.Allowed {
			t.Fatal("method mismatch allowed")
		}
	}
	tunnel := basePolicy(contract.HTTPAllowTunnel)
	tunnel.Destination.Port = 80
	p := basePolicy(contract.HTTPBlockRequests)
	p.Request.Origin.Scheme = "http"
	p.Request.Origin.Port = 80
	s := snapshot(grant(t, 10, tunnel), grant(t, 11, p))
	s.Default = contract.HTTPDefaultAllow
	e := evaluator(t, s)
	r, err := ParseRequest("http://api.example.com/write", "GET", "api.example.com", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Request(r, publicFacts())
	if err != nil || got.Allowed || got.Reason != contract.HTTPReasonRequestBlock {
		t.Fatal("plain HTTP bypassed request policy", got, err)
	}
}

func TestHTTPCredentialContainment(t *testing.T) {
	for _, tc := range []struct {
		boundary, scope string
		bp, sp          uint16
		want            bool
	}{
		{"api.example.com", "api.example.com", 443, 443, true}, {"*.example.com", "api.example.com", 443, 443, true},
		{"*.example.com", "a.b.example.com", 443, 443, true}, {"*.example.com", "example.com", 443, 443, false},
		{"*.example.com", "*.sub.example.com", 443, 443, true}, {"*.sub.example.com", "*.example.com", 443, 443, false},
		{"api.example.com", "*.example.com", 443, 443, false}, {"*.example.com", "*.example.com", 443, 8443, false},
		{"*.example.com", "example.com.evil", 443, 443, false}, {"API.EXAMPLE.COM", "api.example.com", 8443, 8443, true},
	} {
		p := basePolicy(contract.HTTPAllowRequests)
		p.Request.Origin.Host = tc.scope
		p.Request.Origin.Port = tc.sp
		c, err := Compile(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := CredentialContains(credential(80, tc.boundary, tc.bp), c)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %v %v", tc, got, err)
		}
	}
	p := basePolicy(contract.HTTPAllowRequests)
	p.Request.Origin.Host = "*.example.com"
	p.CredentialID = stringp(id(80))
	c := credential(80, "a.example.com", 443)
	c.Origins = append(c.Origins, contract.HTTPOriginSelector{Scheme: "https", Host: "b.example.com", Port: 443})
	s := snapshot(grant(t, 10, p))
	s.Credentials = []Credential{c}
	if _, err := New(s); err == nil {
		t.Fatal("finite overlap mistaken for containment")
	}
}

func TestHTTPCredentialSelectionAndTunnelBypass(t *testing.T) {
	plain := grant(t, 10, basePolicy(contract.HTTPAllowRequests))
	p := basePolicy(contract.HTTPAllowRequests)
	p.CredentialID = stringp(id(80))
	a, b := grant(t, 11, p), grant(t, 12, p)
	p.CredentialID = stringp(id(81))
	c := grant(t, 13, p)
	s := snapshot(plain, a, b)
	s.Credentials = []Credential{credential(80, "*.example.com", 443), credential(81, "api.example.com", 443)}
	e := evaluator(t, s)
	got, err := e.Request(request(t, "/"), publicFacts())
	if err != nil || !got.Allowed || got.Credential == nil || got.Credential.ID != id(80) || got.CredentialGrant.ID != id(11) {
		t.Fatal(got, err)
	}
	s.Grants = append(s.Grants, c)
	e = evaluator(t, s)
	got, err = e.Request(request(t, "/"), publicFacts())
	if err != nil || got.Allowed || got.Reason != contract.HTTPReasonCredentialConflict || got.ConflictCredential.ID != id(81) {
		t.Fatal(got, err)
	}
	s.Grants = append(s.Grants, grant(t, 14, basePolicy(contract.HTTPAllowTunnel)))
	e = evaluator(t, s)
	got, err = e.Connect(request(t, "/").destination, publicFacts())
	if err != nil || !got.Allowed || got.Credential != nil || got.Transport != contract.HTTPTransportTunnel {
		t.Fatal(got, err)
	}
	s.Grants = []Grant{plain, a}
	s.Credentials[0].Available = false
	s.Default = contract.HTTPDefaultAllow
	e = evaluator(t, s)
	got, err = e.Request(request(t, "/"), publicFacts())
	if err != nil || got.Allowed || got.Reason != contract.HTTPReasonCredentialUnavailable {
		t.Fatal("fell back uninjected", got, err)
	}
	s.Credentials = nil
	if _, err = New(s); err == nil {
		t.Fatal("missing required credential accepted")
	}
}

func TestHTTPPrivateAuthorityIsolation(t *testing.T) {
	facts := AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("10.0.0.1")}}
	p := basePolicy(contract.HTTPAllowRequests)
	p.AllowPrivate = boolp(true)
	p.Request.Path = contract.HTTPPathSelector{Kind: contract.HTTPPathPrefix, Value: "/private"}
	g := grant(t, 10, p)
	tunnel := basePolicy(contract.HTTPAllowTunnel)
	tunnel.AllowPrivate = boolp(true)
	for _, tc := range []struct {
		name string
		gs   []Grant
		path string
		want bool
	}{
		{"default", nil, "/private", false}, {"matching", []Grant{g}, "/private/ok", true},
		{"nonmatching path", []Grant{g}, "/public", false}, {"tunnel not request", []Grant{grant(t, 11, tunnel)}, "/private", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := snapshot(tc.gs...)
			s.Default = contract.HTTPDefaultAllow
			got, err := evaluator(t, s).Request(request(t, tc.path), facts)
			if err != nil || got.Allowed != tc.want {
				t.Fatal(got, err)
			}
		})
	}
	foreign := g
	foreign.PrincipalID = id(2)
	s := snapshot(foreign)
	s.Default = contract.HTTPDefaultAllow
	got, err := evaluator(t, s).Request(request(t, "/private"), facts)
	if err != nil || got.Allowed {
		t.Fatal("foreign permission leaked", got, err)
	}
	s = snapshot(g, grant(t, 11, basePolicy(contract.HTTPAllowTunnel)))
	got, err = evaluator(t, s).Connect(request(t, "/").destination, facts)
	if err != nil || got.Allowed {
		t.Fatal("request permission leaked to tunnel", got, err)
	}
	s = snapshot(grant(t, 11, tunnel))
	got, err = evaluator(t, s).Connect(request(t, "/").destination, facts)
	if err != nil || !got.Allowed {
		t.Fatal(got, err)
	}
	// Gateway endpoint and any forbidden member override private authority.
	s = snapshot(g)
	facts.GatewayListeners = []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:443")}
	got, err = evaluator(t, s).Request(request(t, "/private"), facts)
	if err != nil || got.Reason != contract.HTTPReasonAddressForbidden || got.Allowed {
		t.Fatal(got, err)
	}
	facts.GatewayListeners = nil
	facts.Addresses = append(facts.Addresses, netip.MustParseAddr("169.254.169.254"))
	got, err = evaluator(t, s).Request(request(t, "/private"), facts)
	if err != nil || got.Allowed || got.Reason != contract.HTTPReasonAddressForbidden {
		t.Fatal(got, err)
	}
}

func TestHTTPStrictPolicyAndInvalidCombinations(t *testing.T) {
	good := `{"version":1,"type":"allow_tunnel","destination":{"host":"example.com","port":443}}`
	if _, err := DecodePolicy([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`null`, strings.Replace(good, `"version":1`, `"version":2`, 1), strings.Replace(good, `"version":1`, `"Version":1`, 1),
		strings.Replace(good, `"version":1`, `"version":1,"version":1`, 1), good + `{}`, strings.Replace(good, `"port":443`, `"port":null`, 1),
		strings.Replace(good, `"port":443`, `"port":443,"scheme":"https"`, 1), strings.Replace(good, `"port":443`, `"port":65536`, 1),
		strings.Replace(good, `"port":443`, `"port":0`, 1), strings.Replace(good, `"host":"example.com"`, `"host":"a*b.example.com"`, 1),
		strings.Replace(good, `"type":"allow_tunnel"`, `"type":"allow_tunnel","credential_id":"`+id(80)+`"`, 1),
		strings.Replace(good, `"type":"allow_tunnel"`, `"type":"block_destination","allow_private":false`, 1),
		strings.Replace(good, `"type":"allow_tunnel"`, `"type":"allow_tunnel","allow_private":null`, 1),
		strings.Repeat(" ", contract.HTTPPolicyBytes) + good,
	} {
		if _, err := DecodePolicy([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, mutate := range []func(*contract.HTTPPolicy){
		func(p *contract.HTTPPolicy) { p.Version = 0 }, func(p *contract.HTTPPolicy) { p.Request.Methods = contract.HTTPMethods{} },
		func(p *contract.HTTPPolicy) { p.Request.Methods.Values = []string{"GET"} }, func(p *contract.HTTPPolicy) { p.Request.Methods = contract.HTTPMethods{Values: []string{"get"}} },
		func(p *contract.HTTPPolicy) { p.Request.Methods = contract.HTTPMethods{Values: []string{"GET", "GET"}} },
		func(p *contract.HTTPPolicy) { p.Request.Methods = contract.HTTPMethods{Values: []string{"CONNECT"}} },
		func(p *contract.HTTPPolicy) { p.Request.Path.Value = "/not-any" }, func(p *contract.HTTPPolicy) { p.Request.Origin.Port = 0 },
		func(p *contract.HTTPPolicy) { p.Request.Origin.Host = "**.example.com" }, func(p *contract.HTTPPolicy) { p.Request.Origin.Host = "*.127.0.0.1" },
		func(p *contract.HTTPPolicy) { p.CredentialID = stringp(id(80)); p.Request.Origin.Scheme = "http" },
		func(p *contract.HTTPPolicy) {
			p.Destination = &contract.HTTPDestinationSelector{Host: "example.com", Port: 443}
		},
	} {
		p := basePolicy(contract.HTTPAllowRequests)
		mutate(&p)
		if _, err := Compile(p); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}

func TestHTTPSelectorUnionPresence(t *testing.T) {
	prefix := `{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},`
	for _, tc := range []struct {
		selectors string
		valid     bool
	}{
		{`"methods":{"any":true},"path":{"kind":"any"}`, true},
		{`"methods":{"values":["GET"]},"path":{"kind":"exact","value":"/"}`, true},
		{`"methods":{"any":false,"values":["GET"]},"path":{"kind":"any"}`, false},
		{`"methods":{"any":true,"values":[]},"path":{"kind":"any"}`, false},
		{`"methods":{"any":false},"path":{"kind":"any"}`, false},
		{`"methods":{"values":[]},"path":{"kind":"any"}`, false},
		{`"methods":{"any":true},"path":{"kind":"any","value":""}`, false},
		{`"methods":{"any":true},"path":{"value":"","kind":"any"}`, false},
		{`"methods":{"any":true},"path":{"kind":"exact"}`, false},
	} {
		_, err := DecodePolicy([]byte(prefix + tc.selectors + `}}`))
		if (err == nil) != tc.valid {
			t.Fatalf("selector union %s: %v", tc.selectors, err)
		}
	}
}

func TestHTTPBoundsSnapshotAndSafeEvidence(t *testing.T) {
	g := grant(t, 10, basePolicy(contract.HTTPAllowRequests))
	s := snapshot(g)
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.PolicyRevision = 0 }, func(s *Snapshot) { s.DefaultRevision = 0 }, func(s *Snapshot) { s.Default = "unknown" },
		func(s *Snapshot) { s.Grants = []Grant{g, g} }, func(s *Snapshot) { s.Grants = []Grant{{Ref: ref(11), PrincipalID: id(1)}} },
		func(s *Snapshot) { s.Grants = make([]Grant, contract.HTTPPolicyGrants+1) },
		func(s *Snapshot) { s.Credentials = make([]Credential, contract.HTTPPolicyCredentials+1) },
	} {
		copy := s
		mutate(&copy)
		if _, err := New(copy); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
	e := evaluator(t, s)
	s.Grants[0].PrincipalID = id(2)
	got, err := e.Request(request(t, "/sensitive?q=secret"), publicFacts())
	if err != nil || !got.Allowed {
		t.Fatal("snapshot mutated", got, err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 2048 || strings.Contains(string(raw), "sensitive") || strings.Contains(string(raw), "secret") || got.PolicyRevision != 99 || got.DefaultRevision != 3 || got.Grant.Revision != 11 {
		t.Fatal("unsafe/incomplete evidence", string(raw))
	}
	for _, f := range []AddressFacts{{}, {Complete: true}, {Complete: true, Addresses: make([]netip.Addr, contract.HTTPAddressFacts+1)}} {
		if _, err := e.Request(request(t, "/"), f); err == nil {
			t.Fatal("missing/oversized address evidence accepted")
		}
	}
}
