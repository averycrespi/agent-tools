// Package credentials resolves ${cred.<name>} references in rule injections
// and enforces the host binding every credential carries.
//
// Two sources, one enforcement path. A credential comes from either the OS
// keychain or an env_credentials entry in config.json, and both produce the
// same Record — a value plus a list of bound host globs — checked by the same
// code. An earlier design had rules reference ${keychain.*} and ${env.*}
// separately, which silently exempted environment-sourced credentials from
// host binding. Collapsing to one reference form and one check makes that
// class of gap impossible rather than documented (AC-9).
//
// Host binding is a second, independent check. A rule's host glob decides
// whether the rule fires; a credential's bound globs decide whether that
// credential may travel to that host. Both must pass, because otherwise a
// single rule-authoring slip sends a real token wherever the rule now matches.
package credentials

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/http-broker/internal/hostmatch"
	"github.com/averycrespi/agent-tools/http-broker/internal/hostnorm"
)

// Sentinel errors. Callers distinguish these to pick the audit tag and the
// X-Http-Broker-Reason value.
var (
	// ErrNotFound means no source holds a credential by that name.
	ErrNotFound = errors.New("credential not found")
	// ErrHostScope means the credential exists but is not bound to the host
	// the request is going to.
	ErrHostScope = errors.New("credential is not bound to this host")
	// ErrUnavailable means a source could not be consulted at all — a locked
	// or absent keychain, typically.
	ErrUnavailable = errors.New("credential source unavailable")
	// ErrInvalidValue means the resolved value cannot be placed in a header.
	ErrInvalidValue = errors.New("credential value is not a valid header value")
)

// Record is a credential and the hosts it may be sent to.
type Record struct {
	Value string
	// Hosts are host globs, already normalised. A record with no hosts is
	// invalid: every credential carries scope.
	Hosts []string
}

// Source supplies credential records by name.
type Source interface {
	// Get returns the record for name, or ErrNotFound.
	Get(name string) (Record, error)
	// Kind names the source for diagnostics and the startup warning.
	Kind() string
}

// refPattern matches a ${cred.<name>} reference. Names are restricted to
// characters that cannot be confused with template syntax.
var refPattern = regexp.MustCompile(`\$\{cred\.([A-Za-z0-9_.-]+)\}`)

// ValidName reports whether s is a usable credential name.
func ValidName(s string) bool {
	if s == "" {
		return false
	}
	return regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(s)
}

// References returns the credential names referenced by a template string.
func References(template string) []string {
	matches := refPattern.FindAllStringSubmatch(template, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// ReferencesIn returns every credential name referenced across a set of
// header templates, deduplicated.
func ReferencesIn(templates map[string]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, tmpl := range templates {
		for _, name := range References(tmpl) {
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

// cacheEntry is a resolved record held for a short TTL.
type cacheEntry struct {
	record  Record
	expires time.Time
}

// Resolver resolves references against an ordered list of sources.
//
// Resolution is lazy and cached for a short TTL. Lazy resolution is what lets
// the proxy start under launchd without a keychain prompt blocking startup: an
// unavailable keychain fails individual requests with an audited error rather
// than preventing the process from serving at all.
type Resolver struct {
	sources []Source
	ttl     time.Duration
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// DefaultTTL is how long a resolved credential is reused before the source is
// consulted again. Short enough that a rotated secret takes effect promptly,
// long enough that a burst of requests does not hammer the keychain.
const DefaultTTL = 30 * time.Second

// New returns a Resolver over the given sources, consulted in order.
func New(sources ...Source) *Resolver {
	return &Resolver{
		sources: sources,
		ttl:     DefaultTTL,
		now:     time.Now,
		cache:   make(map[string]cacheEntry),
	}
}

// SetTTL overrides the cache TTL.
func (r *Resolver) SetTTL(d time.Duration) { r.ttl = d }

// ClearCache drops every cached record. Called on SIGHUP so an operator who
// has just rotated a credential does not wait out the TTL.
func (r *Resolver) ClearCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = make(map[string]cacheEntry)
}

// lookup returns the record for name, consulting the cache first.
func (r *Resolver) lookup(name string) (Record, error) {
	r.mu.Lock()
	if e, ok := r.cache[name]; ok && r.now().Before(e.expires) {
		r.mu.Unlock()
		return e.record, nil
	}
	r.mu.Unlock()

	var unavailable error
	for _, src := range r.sources {
		record, err := src.Get(name)
		switch {
		case err == nil:
			r.mu.Lock()
			r.cache[name] = cacheEntry{record: record, expires: r.now().Add(r.ttl)}
			r.mu.Unlock()
			return record, nil
		case errors.Is(err, ErrNotFound):
			continue
		default:
			// Remember the failure but keep trying other sources: a locked
			// keychain should not mask a working env_credentials entry.
			if unavailable == nil {
				unavailable = err
			}
		}
	}

	if unavailable != nil {
		return Record{}, unavailable
	}
	return Record{}, fmt.Errorf("%w: %q", ErrNotFound, name)
}

// Resolve returns the value of one credential, enforcing host scope.
//
// host must already be normalised.
func (r *Resolver) Resolve(name, host string) (string, error) {
	record, err := r.lookup(name)
	if err != nil {
		return "", err
	}
	if err := checkHostScope(name, record, host); err != nil {
		return "", err
	}
	return record.Value, nil
}

// checkHostScope enforces the credential's bound host globs.
//
// Matching goes through hostnorm, the same path rule matching uses, so rule
// scope and credential scope can never disagree about what a glob means (D16).
func checkHostScope(name string, record Record, host string) error {
	if len(record.Hosts) == 0 {
		// Defence in depth: loading rejects an unbound credential, so reaching
		// here means a source produced one anyway. Fail closed.
		return fmt.Errorf("%w: %q has no bound hosts", ErrHostScope, name)
	}
	for _, pattern := range record.Hosts {
		if hostnorm.MatchHostGlob(pattern, host) {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is bound to %s, not %q",
		ErrHostScope, name, strings.Join(record.Hosts, ", "), host)
}

// Expanded is the result of expanding one rule's injection templates.
type Expanded struct {
	// Headers are the fully expanded header values, ready to write.
	Headers map[string]string
	// Names are the credentials that were used, for the audit row's
	// credential_ref column. Values never appear.
	Names []string
}

// ExpandAll expands every template for a rule, resolving and host-checking
// every credential BEFORE returning anything (D17).
//
// This is the atomicity contract: a caller that writes headers only from a
// successful return can never dispatch a partially injected request. If the
// second of two credentials fails its host check, the first must not already
// be sitting on an upstream request — the point of the check is that the token
// does not reach the wire, and "we set it then errored" would defeat that.
func (r *Resolver) ExpandAll(templates map[string]string, host string) (Expanded, error) {
	// Resolve every distinct reference first, so a failure aborts before any
	// value is placed into a header string.
	names := ReferencesIn(templates)
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, err := r.Resolve(name, host)
		if err != nil {
			return Expanded{}, err
		}
		values[name] = value
	}

	headers := make(map[string]string, len(templates))
	for header, tmpl := range templates {
		expanded := refPattern.ReplaceAllStringFunc(tmpl, func(ref string) string {
			m := refPattern.FindStringSubmatch(ref)
			return values[m[1]]
		})
		if err := ValidateHeaderValue(expanded); err != nil {
			// Never include the value: the error string reaches logs and the
			// audit row (AC-10).
			return Expanded{}, fmt.Errorf("header %q: %w", header, err)
		}
		headers[header] = expanded
	}

	return Expanded{Headers: headers, Names: names}, nil
}

// ValidateHeaderValue rejects a value that cannot safely be written into a
// header.
//
// A CR or LF would split the header block and let a credential value inject
// arbitrary headers into the upstream request; other control bytes are
// rejected because a credential should never contain them and their presence
// signals a corrupt or wrong secret.
func ValidateHeaderValue(v string) error {
	for i := range len(v) {
		c := v[i]
		if c == '\r' || c == '\n' {
			return fmt.Errorf("%w: contains a line break", ErrInvalidValue)
		}
		if c < 0x20 && c != '\t' {
			return fmt.Errorf("%w: contains a control byte", ErrInvalidValue)
		}
		if c == 0x7f {
			return fmt.Errorf("%w: contains a DEL byte", ErrInvalidValue)
		}
	}
	return nil
}

// ValidateHostGlobs reports whether a credential's bound host globs are
// acceptable, without returning the normalised form. Config loading uses it so
// an env_credentials entry is rejected at startup rather than at first use.
func ValidateHostGlobs(hosts []string) error {
	_, err := NormalizeHosts(hosts)
	return err
}

// NormalizeHosts normalises and validates a credential's bound host globs.
func NormalizeHosts(hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return nil, errors.New("at least one bound host is required; every credential carries host scope")
	}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		normalized, err := hostnorm.NormalizeGlob(h)
		if err != nil {
			return nil, fmt.Errorf("host %q: %w", h, err)
		}
		if normalized == "*" || normalized == "**" {
			return nil, fmt.Errorf("host %q matches every host; bind the credential to the hosts it is for", h)
		}
		// The same public-suffix rejection rule loading applies. Binding a
		// credential to "*.com" is the footgun host binding exists to prevent,
		// and it has to be caught here rather than only in the CLI — otherwise
		// an env_credentials entry in config.json reaches the request path
		// unchecked, which is exactly the single-enforcement-path gap this
		// package was written to make impossible.
		if isSuffix, suffix := hostmatch.MatchesPublicSuffix(normalized); isSuffix {
			return nil, fmt.Errorf("host %q reduces to the public suffix %q, binding the credential to every host under a registry-controlled TLD", h, suffix)
		}
		out = append(out, normalized)
	}
	return out, nil
}
