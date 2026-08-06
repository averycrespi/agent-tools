package credentials

import (
	"fmt"
	"os"
	"sort"
	"sync"
)

// EnvSpec is one env_credentials entry: the variable to read and the hosts the
// value may be sent to.
type EnvSpec struct {
	Var   string
	Hosts []string
}

// Env is a Source reading values from process environment variables.
//
// It exists so the e2e suite never touches a developer's real keychain, and so
// shell-run development and headless Linux hosts stay workable. It is not a
// weaker path: entries carry bound hosts exactly as keychain records do and go
// through the identical check. `serve` logs a prominent startup warning naming
// every credential sourced this way, because the value sits in the process
// environment where the keychain design is trying not to put it.
// Specs are swappable so a SIGHUP can apply an edited env_credentials block.
// The mutex is what makes that safe: reload runs on the signal goroutine while
// requests are resolving.
type Env struct {
	mu     sync.RWMutex
	specs  map[string]EnvSpec
	lookup func(string) (string, bool)
}

// NewEnv returns a Source over the given env_credentials specs.
func NewEnv(specs map[string]EnvSpec) *Env {
	return &Env{specs: specs, lookup: os.LookupEnv}
}

// SetSpecs replaces the configured specs. Called on SIGHUP, so an operator who
// adds or rebinds an env_credentials entry does not have to restart.
func (e *Env) SetSpecs(specs map[string]EnvSpec) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.specs = specs
}

func (e *Env) spec(name string) (EnvSpec, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	spec, ok := e.specs[name]
	return spec, ok
}

// Kind implements Source.
func (e *Env) Kind() string { return SourceEnv }

// Names returns the configured credential names, sorted. `serve` uses this for
// its startup warning.
func (e *Env) Names() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]string, 0, len(e.specs))
	for name := range e.specs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a name is configured, without building the name list.
func (e *Env) Has(name string) bool {
	_, ok := e.spec(name)
	return ok
}

// Get implements Source.
func (e *Env) Get(name string) (Record, error) {
	spec, ok := e.spec(name)
	if !ok {
		return Record{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}

	value, ok := e.lookup(spec.Var)
	if !ok {
		return Record{}, fmt.Errorf("%w: env_credentials.%s names $%s, which is not set in the proxy's environment",
			ErrUnavailable, name, spec.Var)
	}
	if value == "" {
		return Record{}, fmt.Errorf("%w: env_credentials.%s names $%s, which is set but empty",
			ErrUnavailable, name, spec.Var)
	}

	hosts, err := NormalizeHosts(spec.Hosts)
	if err != nil {
		return Record{}, fmt.Errorf("%w: env_credentials.%s: %w", ErrUnavailable, name, err)
	}
	return Record{Value: value, Hosts: hosts}, nil
}

// Describe returns metadata for a configured env credential, without its
// value.
func (e *Env) Describe(name string) (Metadata, error) {
	spec, ok := e.spec(name)
	if !ok {
		return Metadata{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	hosts, err := NormalizeHosts(spec.Hosts)
	if err != nil {
		return Metadata{}, err
	}

	// Report the value length when the variable is set, so an operator can
	// tell a missing secret from a present one without printing it.
	bytes := 0
	if value, set := e.lookup(spec.Var); set {
		bytes = len(value)
	}
	return Metadata{Name: name, Source: e.Kind(), Hosts: hosts, Bytes: bytes}, nil
}
