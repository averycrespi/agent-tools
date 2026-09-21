package httppolicy

import (
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func TestHTTPExactBounds(t *testing.T) {
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if len(host) != 253 {
		t.Fatal("fixture")
	}
	if _, err := NewDestination(host, 65535); err != nil {
		t.Fatal("maximum host/port", err)
	}
	p := basePolicy(contract.HTTPAllowRequests)
	p.Request.Path = contract.HTTPPathSelector{Kind: contract.HTTPPathExact, Value: "/" + strings.Repeat("a", contract.HTTPPathBytes-1)}
	if _, err := Compile(p); err != nil {
		t.Fatal("maximum path", err)
	}
	p.Request.Path.Value += "a"
	if _, err := Compile(p); err == nil {
		t.Fatal("oversized path")
	}
	p = basePolicy(contract.HTTPAllowRequests)
	p.Request.Methods.Any = false
	for i := range contract.HTTPPolicyMethods {
		p.Request.Methods.Values = append(p.Request.Methods.Values, fmt.Sprintf("M%031d", i))
	}
	if _, err := Compile(p); err != nil {
		t.Fatal("maximum methods", err)
	}
	p.Request.Methods.Values = append(p.Request.Methods.Values, "EXTRA")
	if _, err := Compile(p); err == nil {
		t.Fatal("oversized method set")
	}
	p.Request.Methods.Values = []string{strings.Repeat("A", contract.HTTPMethodBytes+1)}
	if _, err := Compile(p); err == nil {
		t.Fatal("oversized method")
	}
	c := credential(80, "example.com", 443)
	c.Origins = make([]contract.HTTPOriginSelector, contract.HTTPPolicyOrigins)
	for i := range c.Origins {
		c.Origins[i] = contract.HTTPOriginSelector{Scheme: "https", Host: "example.com", Port: uint16(i + 1)}
	}
	if _, err := compileCredential(c); err != nil {
		t.Fatal("maximum origins", err)
	}
	c.Origins = append(c.Origins, c.Origins[0])
	if _, err := compileCredential(c); err == nil {
		t.Fatal("oversized origins")
	}
	prefix := "https://api.example.com:443/?"
	raw := prefix + strings.Repeat("a", contract.HTTPTargetBytes-len(prefix))
	if _, err := ParseRequest(raw, "GET", "api.example.com", "", nil); err != nil {
		t.Fatal("maximum URI", err)
	}
	if _, err := ParseRequest(raw+"a", "GET", "api.example.com", "", nil); err == nil {
		t.Fatal("oversized URI")
	}
	expanded := strings.Replace(raw, ":443", "", 1) + "aaaa"
	if _, err := ParseRequest(expanded, "GET", "api.example.com", "", nil); err == nil {
		t.Fatal("canonical expansion exceeded URI bound")
	}
	policy := `{"version":1,"type":"allow_tunnel","destination":{"host":"example.com","port":443}}`
	policy += strings.Repeat(" ", contract.HTTPPolicyBytes-len(policy))
	if _, err := DecodePolicy([]byte(policy)); err != nil {
		t.Fatal("maximum policy bytes", err)
	}
	if _, err := DecodePolicy([]byte(policy + " ")); err == nil {
		t.Fatal("oversized policy bytes")
	}
}

func TestHTTPMaximumSnapshotAndEvidenceOrder(t *testing.T) {
	p := basePolicy(contract.HTTPAllowRequests)
	p.AllowPrivate = boolp(true)
	p.CredentialID = stringp(id(8000))
	g := grant(t, 10, p)
	s := snapshot()
	s.Grants = make([]Grant, contract.HTTPPolicyGrants)
	for i := range s.Grants {
		s.Grants[i] = g
		s.Grants[i].Ref = ref(i + 10)
	}
	for i := range contract.HTTPPolicyCredentials {
		s.Credentials = append(s.Credentials, credential(8000+i, "*.example.com", 443))
	}
	// Conflicting requirements late in the input cannot be hidden by truncation.
	p.CredentialID = stringp(id(8001))
	s.Grants[len(s.Grants)-1] = grant(t, 7000, p)
	want, err := evaluator(t, s).Request(request(t, "/"), publicFacts())
	if err != nil || want.Reason != contract.HTTPReasonCredentialConflict {
		t.Fatal(want, err)
	}
	for i, j := 0, len(s.Grants)-1; i < j; i, j = i+1, j-1 {
		s.Grants[i], s.Grants[j] = s.Grants[j], s.Grants[i]
	}
	got, err := evaluator(t, s).Request(request(t, "/"), publicFacts())
	if err != nil || !reflect.DeepEqual(want, got) {
		t.Fatal("conflict/private evidence reordered", got, err)
	}
	facts := publicFacts()
	facts.Addresses = make([]netip.Addr, contract.HTTPAddressFacts)
	for i := range facts.Addresses {
		facts.Addresses[i] = netip.MustParseAddr("8.8.8.8")
	}
	e := evaluator(t, snapshot(grant(t, 10, basePolicy(contract.HTTPAllowRequests))))
	if got, err := e.Request(request(t, "/"), facts); err != nil || !got.Allowed {
		t.Fatal("maximum facts", got, err)
	}
	facts.GatewayListeners = make([]netip.AddrPort, contract.HTTPAddressFacts)
	for i := range facts.GatewayListeners {
		facts.GatewayListeners[i] = netip.MustParseAddrPort("127.0.0.1:8210")
	}
	if got, err := e.Request(request(t, "/"), facts); err != nil || !got.Allowed {
		t.Fatal("maximum listeners", got, err)
	}
	facts.GatewayListeners = append(facts.GatewayListeners, facts.GatewayListeners[0])
	if _, err := e.Request(request(t, "/"), facts); err == nil {
		t.Fatal("oversized listener facts")
	}
}
