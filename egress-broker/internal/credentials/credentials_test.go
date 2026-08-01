package credentials_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/egress-broker/internal/credentials"
)

// fakeSource is a Source backed by a map, with an injectable failure.
type fakeSource struct {
	kind    string
	records map[string]credentials.Record
	err     error
	calls   int
}

func (f *fakeSource) Kind() string { return f.kind }

func (f *fakeSource) Get(name string) (credentials.Record, error) {
	f.calls++
	if f.err != nil {
		return credentials.Record{}, f.err
	}
	r, ok := f.records[name]
	if !ok {
		return credentials.Record{}, fmt.Errorf("%w: %q", credentials.ErrNotFound, name)
	}
	return r, nil
}

func newResolver(records map[string]credentials.Record) (*credentials.Resolver, *fakeSource) {
	src := &fakeSource{kind: "fake", records: records}
	return credentials.New(src), src
}

func TestReferences(t *testing.T) {
	cases := []struct {
		template string
		want     []string
	}{
		{"Bearer ${cred.gh_bot}", []string{"gh_bot"}},
		{"${cred.a} and ${cred.b}", []string{"a", "b"}},
		{"no references here", nil},
		{"${cred.with-dash}", []string{"with-dash"}},
		{"${cred.with_underscore}", []string{"with_underscore"}},
		{"${cred.with.dot}", []string{"with.dot"}},
		{"${notcred.x}", nil},
		{"${cred.}", nil},
	}
	for _, tc := range cases {
		got := credentials.References(tc.template)
		if len(got) != len(tc.want) {
			t.Errorf("References(%q) = %v, want %v", tc.template, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("References(%q) = %v, want %v", tc.template, got, tc.want)
				break
			}
		}
	}
}

func TestResolveHostScope(t *testing.T) {
	r, _ := newResolver(map[string]credentials.Record{
		"gh_bot":   {Value: "SENTINEL-GH", Hosts: []string{"api.github.com"}},
		"wildcard": {Value: "SENTINEL-WC", Hosts: []string{"*.github.com"}},
		"multi":    {Value: "SENTINEL-MULTI", Hosts: []string{"api.github.com", "api.stripe.com"}},
	})

	cases := []struct {
		name    string
		cred    string
		host    string
		wantErr bool
	}{
		{"exact match", "gh_bot", "api.github.com", false},
		{"different host", "gh_bot", "api.stripe.com", true},
		{"subdomain does not match an exact binding", "gh_bot", "x.api.github.com", true},
		{"suffix confusion", "gh_bot", "api.github.com.attacker.com", true},

		{"wildcard matches a subdomain", "wildcard", "api.github.com", false},
		{"wildcard does not match the apex", "wildcard", "github.com", true},
		{"wildcard does not cross a label", "wildcard", "a.b.github.com", true},
		{"wildcard suffix confusion", "wildcard", "api.github.com.attacker.com", true},

		{"multi-bound first", "multi", "api.github.com", false},
		{"multi-bound second", "multi", "api.stripe.com", false},
		{"multi-bound neither", "multi", "api.gitlab.com", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := r.Resolve(tc.cred, tc.host)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q, %q) = %q, want a host-scope refusal", tc.cred, tc.host, value)
				}
				if !errors.Is(err, credentials.ErrHostScope) {
					t.Fatalf("error %v, want it to wrap ErrHostScope", err)
				}
				if strings.Contains(err.Error(), "SENTINEL") {
					t.Errorf("the error must not contain the credential value: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q, %q) = %v, want it allowed", tc.cred, tc.host, err)
			}
		})
	}
}

func TestResolveNotFound(t *testing.T) {
	r, _ := newResolver(map[string]credentials.Record{})

	_, err := r.Resolve("absent", "api.github.com")
	if err == nil {
		t.Fatal("Resolve of an absent credential = nil, want an error")
	}
	if !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("error %v, want it to wrap ErrNotFound", err)
	}
}

// TestUnboundCredentialFailsClosed is defence in depth: loading rejects an
// unbound credential, so a source producing one means something is wrong.
func TestUnboundCredentialFailsClosed(t *testing.T) {
	r, _ := newResolver(map[string]credentials.Record{
		"unbound": {Value: "SENTINEL", Hosts: nil},
	})

	_, err := r.Resolve("unbound", "api.github.com")
	if err == nil {
		t.Fatal("a credential with no bound hosts should never resolve")
	}
	if !errors.Is(err, credentials.ErrHostScope) {
		t.Fatalf("error %v, want it to wrap ErrHostScope", err)
	}
}

// TestExpandAllAtomicity is D17. When the second credential fails, the first
// must not have been placed anywhere the caller could dispatch.
func TestExpandAllAtomicity(t *testing.T) {
	r, _ := newResolver(map[string]credentials.Record{
		"good": {Value: "GOOD-SENTINEL", Hosts: []string{"api.github.com"}},
		"bad":  {Value: "BAD-SENTINEL", Hosts: []string{"api.stripe.com"}},
	})

	templates := map[string]string{
		"Authorization": "Bearer ${cred.good}",
		"X-Other":       "${cred.bad}",
	}

	got, err := r.ExpandAll(templates, "api.github.com")
	if err == nil {
		t.Fatal("ExpandAll should fail when one credential is out of scope")
	}
	if !errors.Is(err, credentials.ErrHostScope) {
		t.Fatalf("error %v, want it to wrap ErrHostScope", err)
	}
	if got.Headers != nil {
		t.Errorf("a failed ExpandAll must return no headers at all, got %v", got.Headers)
	}
	for _, sentinel := range []string{"GOOD-SENTINEL", "BAD-SENTINEL"} {
		if strings.Contains(err.Error(), sentinel) {
			t.Errorf("the error must not contain a credential value: %q", err)
		}
	}
}

// TestExpandAllAtomicityOnMissing covers the same contract for an absent
// credential rather than an out-of-scope one.
func TestExpandAllAtomicityOnMissing(t *testing.T) {
	r, _ := newResolver(map[string]credentials.Record{
		"good": {Value: "GOOD-SENTINEL", Hosts: []string{"api.github.com"}},
	})

	got, err := r.ExpandAll(map[string]string{
		"Authorization": "Bearer ${cred.good}",
		"X-Other":       "${cred.absent}",
	}, "api.github.com")

	if err == nil {
		t.Fatal("ExpandAll should fail when a referenced credential is absent")
	}
	if got.Headers != nil {
		t.Errorf("a failed ExpandAll must return no headers, got %v", got.Headers)
	}
}

func TestExpandAllSuccess(t *testing.T) {
	r, _ := newResolver(map[string]credentials.Record{
		"gh_bot": {Value: "ghp_secret", Hosts: []string{"api.github.com"}},
	})

	got, err := r.ExpandAll(map[string]string{
		"Authorization":        "Bearer ${cred.gh_bot}",
		"X-GitHub-Api-Version": "2022-11-28",
	}, "api.github.com")
	if err != nil {
		t.Fatalf("ExpandAll: %v", err)
	}

	if got.Headers["Authorization"] != "Bearer ghp_secret" {
		t.Errorf("Authorization = %q, want the reference expanded", got.Headers["Authorization"])
	}
	if got.Headers["X-GitHub-Api-Version"] != "2022-11-28" {
		t.Errorf("a literal header should pass through unchanged, got %q", got.Headers["X-GitHub-Api-Version"])
	}
	if len(got.Names) != 1 || got.Names[0] != "gh_bot" {
		t.Errorf("Names = %v, want [gh_bot] for the audit row", got.Names)
	}
}

func TestExpandAllMultipleReferencesInOneHeader(t *testing.T) {
	r, _ := newResolver(map[string]credentials.Record{
		"user": {Value: "alice", Hosts: []string{"api.example.com"}},
		"pass": {Value: "s3cret", Hosts: []string{"api.example.com"}},
	})

	got, err := r.ExpandAll(map[string]string{"X-Combined": "${cred.user}:${cred.pass}"}, "api.example.com")
	if err != nil {
		t.Fatalf("ExpandAll: %v", err)
	}
	if got.Headers["X-Combined"] != "alice:s3cret" {
		t.Errorf("X-Combined = %q, want both references expanded", got.Headers["X-Combined"])
	}
}

// TestExpandAllDoesNotRescanValues: a credential whose value happens to
// contain ${cred.other} must be inserted literally, never treated as a further
// reference. Rescanning would let one credential's value pull in another,
// bypassing that second credential's own host check.
func TestExpandAllDoesNotRescanValues(t *testing.T) {
	r, _ := newResolver(map[string]credentials.Record{
		"outer":  {Value: "${cred.secret}", Hosts: []string{"api.example.com"}},
		"secret": {Value: "MUST-NOT-APPEAR", Hosts: []string{"api.example.com"}},
	})

	got, err := r.ExpandAll(map[string]string{"X-Test": "${cred.outer}"}, "api.example.com")
	if err != nil {
		t.Fatalf("ExpandAll: %v", err)
	}
	if got.Headers["X-Test"] != "${cred.secret}" {
		t.Errorf("X-Test = %q, want the value inserted literally without a second expansion pass", got.Headers["X-Test"])
	}
	if strings.Contains(got.Headers["X-Test"], "MUST-NOT-APPEAR") {
		t.Error("a credential value must never be rescanned for further references")
	}
	// Only the directly referenced credential is recorded, so the audit row
	// does not claim a credential that was never resolved.
	if len(got.Names) != 1 || got.Names[0] != "outer" {
		t.Errorf("Names = %v, want [outer]", got.Names)
	}
}

// TestExpandAllRejectsControlBytes covers header-splitting: a value carrying a
// CRLF would let a credential inject arbitrary headers upstream.
func TestExpandAllRejectsControlBytes(t *testing.T) {
	for _, value := range []string{
		"tok\r\nX-Injected: yes",
		"tok\nX-Injected: yes",
		"tok\rmore",
		"tok\x00more",
		"tok\x01more",
		"tok\x7fmore",
	} {
		r, _ := newResolver(map[string]credentials.Record{
			"bad": {Value: value, Hosts: []string{"api.example.com"}},
		})

		got, err := r.ExpandAll(map[string]string{"X-Test": "${cred.bad}"}, "api.example.com")
		if err == nil {
			t.Errorf("value %q should be rejected as a header value", value)
			continue
		}
		if !errors.Is(err, credentials.ErrInvalidValue) {
			t.Errorf("value %q: error %v, want it to wrap ErrInvalidValue", value, err)
		}
		if got.Headers != nil {
			t.Errorf("value %q: a rejected value must yield no headers", value)
		}
		if strings.Contains(err.Error(), "X-Injected") {
			t.Errorf("the error must not echo the credential value: %q", err)
		}
	}
}

func TestValidateHeaderValueAcceptsNormalValues(t *testing.T) {
	for _, value := range []string{
		"ghp_abc123",
		"Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig",
		"with spaces and = padding",
		"tab\there", // horizontal tab is legal in a header value
		"",
	} {
		if err := credentials.ValidateHeaderValue(value); err != nil {
			t.Errorf("ValidateHeaderValue(%q) = %v, want nil", value, err)
		}
	}
}

func TestCacheReusesWithinTTL(t *testing.T) {
	r, src := newResolver(map[string]credentials.Record{
		"gh_bot": {Value: "v", Hosts: []string{"api.github.com"}},
	})
	r.SetTTL(time.Minute)

	for range 5 {
		if _, err := r.Resolve("gh_bot", "api.github.com"); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}
	if src.calls != 1 {
		t.Errorf("source consulted %d times, want 1: the TTL cache should absorb the rest", src.calls)
	}
}

func TestClearCacheForcesReresolution(t *testing.T) {
	r, src := newResolver(map[string]credentials.Record{
		"gh_bot": {Value: "v", Hosts: []string{"api.github.com"}},
	})
	r.SetTTL(time.Minute)

	if _, err := r.Resolve("gh_bot", "api.github.com"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r.ClearCache()
	if _, err := r.Resolve("gh_bot", "api.github.com"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if src.calls != 2 {
		t.Errorf("source consulted %d times, want 2: ClearCache should force re-resolution", src.calls)
	}
}

// TestCacheDoesNotCacheTheHostDecision: the value is cached, the host check is
// not. A cached credential must still be refused for a host it is not bound to.
func TestCacheDoesNotCacheTheHostDecision(t *testing.T) {
	r, _ := newResolver(map[string]credentials.Record{
		"gh_bot": {Value: "v", Hosts: []string{"api.github.com"}},
	})
	r.SetTTL(time.Minute)

	if _, err := r.Resolve("gh_bot", "api.github.com"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := r.Resolve("gh_bot", "api.stripe.com"); !errors.Is(err, credentials.ErrHostScope) {
		t.Errorf("a cached credential must still be host-checked, got %v", err)
	}
}

// TestUnavailableSourceDoesNotMaskAWorkingOne: a locked keychain must not hide
// a usable env_credentials entry.
func TestUnavailableSourceDoesNotMaskAWorkingOne(t *testing.T) {
	broken := &fakeSource{kind: "keychain", err: fmt.Errorf("%w: locked", credentials.ErrUnavailable)}
	working := &fakeSource{kind: "env_credentials", records: map[string]credentials.Record{
		"gh_bot": {Value: "v", Hosts: []string{"api.github.com"}},
	}}
	r := credentials.New(broken, working)

	if _, err := r.Resolve("gh_bot", "api.github.com"); err != nil {
		t.Errorf("Resolve = %v, want the working source to be consulted", err)
	}
}

func TestUnavailableSourceSurfacesWhenNothingElseHasIt(t *testing.T) {
	broken := &fakeSource{kind: "keychain", err: fmt.Errorf("%w: locked", credentials.ErrUnavailable)}
	r := credentials.New(broken)

	_, err := r.Resolve("gh_bot", "api.github.com")
	if !errors.Is(err, credentials.ErrUnavailable) {
		t.Errorf("error %v, want it to wrap ErrUnavailable rather than being reported as not found", err)
	}
}

func TestNormalizeHosts(t *testing.T) {
	got, err := credentials.NormalizeHosts([]string{"API.GitHub.COM", "*.Stripe.com"})
	if err != nil {
		t.Fatalf("NormalizeHosts: %v", err)
	}
	want := []string{"api.github.com", "*.stripe.com"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("NormalizeHosts = %v, want %v", got, want)
			break
		}
	}

	if _, err := credentials.NormalizeHosts(nil); err == nil {
		t.Error("NormalizeHosts(nil) = nil, want an error: every credential carries host scope")
	}
	for _, pattern := range []string{"*", "**"} {
		if _, err := credentials.NormalizeHosts([]string{pattern}); err == nil {
			t.Errorf("NormalizeHosts(%q) = nil, want it rejected as matching every host", pattern)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"gh_bot", "a-b", "a.b", "A1"} {
		if !credentials.ValidName(name) {
			t.Errorf("ValidName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "with space", "with/slash", "with$dollar", "with}brace"} {
		if credentials.ValidName(name) {
			t.Errorf("ValidName(%q) = true, want false", name)
		}
	}
}

// TestNormalizeHostsRejectsPublicSuffix is the AC-6 gap the whole-change review
// found: rule loading rejected a public-suffix host glob, and so did the
// `credential set` CLI, but an env_credentials entry in config.json reached the
// request path unchecked — the exact single-enforcement-path gap this package
// exists to close.
func TestNormalizeHostsRejectsPublicSuffix(t *testing.T) {
	for _, pattern := range []string{"*.com", "**.co.uk", "com", "co.uk", "*.*.com"} {
		if _, err := credentials.NormalizeHosts([]string{pattern}); err == nil {
			t.Errorf("NormalizeHosts(%q) = nil, want it rejected as a public suffix", pattern)
		}
	}
	// A legitimate binding still works.
	if _, err := credentials.NormalizeHosts([]string{"*.example.com", "api.github.com"}); err != nil {
		t.Errorf("a normal binding should be accepted: %v", err)
	}
	// One bad entry among good ones still fails.
	if _, err := credentials.NormalizeHosts([]string{"api.github.com", "*.com"}); err == nil {
		t.Error("a public suffix alongside a valid host should still be rejected")
	}
}

func TestValidateHostGlobs(t *testing.T) {
	if err := credentials.ValidateHostGlobs([]string{"*.com"}); err == nil {
		t.Error("ValidateHostGlobs should reject a public suffix")
	}
	if err := credentials.ValidateHostGlobs([]string{"api.github.com"}); err != nil {
		t.Errorf("ValidateHostGlobs should accept a normal host: %v", err)
	}
}
