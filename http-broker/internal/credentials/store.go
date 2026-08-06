package credentials

import (
	"errors"
	"fmt"
	"slices"
	"sort"
)

// Sentinel errors callers branch on. None of them ever carries a value.
var (
	// ErrIndexNotUpdated means the credential was stored but its name was not
	// recorded in the index. The credential works; it is not listed until
	// `credential get <name>` re-registers it.
	ErrIndexNotUpdated = errors.New("credential stored but the index was not updated")

	// ErrEnvManaged means the name is an env_credentials entry, which
	// config.json owns; the CLI cannot rebind it.
	ErrEnvManaged = errors.New("credential is managed by env_credentials in config.json")

	// ErrStaleIndexEntry means Delete found no keychain item but did remove the
	// name from the index. Non-fatal: the caller reports a stale-entry cleanup
	// rather than a delete. This distinguishes that case from deleting a name
	// held by neither store, which returns nil.
	ErrStaleIndexEntry = errors.New("removed a stale index entry; no keychain item existed")
)

// Source names for display, and the one place the precedence rule lives.
const (
	SourceKeychain = "keychain"
	SourceEnv      = "env_credentials"
)

// SourceFor decides which source wins for a name and whether an env entry is
// shadowed by a keychain one.
//
// The rule is the resolver's: sources are consulted keychain-first
// (serve.go builds the Resolver in that order), so a name held by both
// resolves through the keychain and an env_credentials entry of the same name
// never reaches a request. Both the CLI's List and the dashboard's credential
// listing call this, so the two can never disagree about precedence — the same
// reason internal/glob keeps a single translation of a pattern.
//
// source is empty when neither source holds the name.
func SourceFor(hasKeychain, hasEnv bool) (source string, envShadowed bool) {
	switch {
	case hasKeychain:
		return SourceKeychain, hasEnv
	case hasEnv:
		return SourceEnv, false
	default:
		return "", false
	}
}

// KeychainSource is the write-capable subset of Keychain that Store needs.
// Exported so tests in other packages can inject a fake; nothing in the
// request path uses it.
type KeychainSource interface {
	Get(name string) (Record, error)
	Set(name, value string, hosts []string) error
	Delete(name string) error
	Describe(name string) (Metadata, error)
}

// Store is the single write path for credentials.
//
// It exists because a credential now lives in two places that must agree: the
// keychain, which holds the value and its host binding, and the index, which
// holds the name so the keychain can be listed at all. Routing every mutation
// through one type is what keeps the two from drifting; the CLI never calls
// Keychain directly.
//
// The keychain envelope remains the sole authority on host scope. Nothing here
// reads or writes a host list to the index.
type Store struct {
	keychain KeychainSource
	index    *Index
	env      *Env
}

// NewStore returns a Store over the real keychain and the default index. env
// may be nil, meaning no env_credentials are configured.
func NewStore(env *Env) *Store {
	return &Store{keychain: NewKeychain(), index: NewIndex(), env: env}
}

// NewStoreWith returns a Store over the given parts. Tests use it to inject a
// fake keychain and a temp-directory index.
func NewStoreWith(k KeychainSource, idx *Index, env *Env) *Store {
	return &Store{keychain: k, index: idx, env: env}
}

// Listing is what `credential list` renders.
type Listing struct {
	// Credentials is one row per credential, sorted by name.
	Credentials []Metadata
	// Pruned names index entries whose keychain item was gone.
	Pruned []string
}

// Set stores a credential and records its name.
//
// The keychain is written first. If that fails the index is left untouched, so
// a failed store never leaves a name claiming a credential that does not
// exist.
func (s *Store) Set(name, value string, hosts []string) error {
	if err := s.keychain.Set(name, value, hosts); err != nil {
		// Returned as-is: callers match the keychain's sentinels on it.
		return err
	}
	if err := s.index.Add(name); err != nil {
		// The credential is stored and usable; only its listing is missing.
		// The index error names the file, never the value.
		return fmt.Errorf("%w: %w", ErrIndexNotUpdated, err)
	}
	return nil
}

// Delete removes a credential from the keychain and its name from the index.
//
// The keychain goes first, mirroring Set. The reverse order is dangerous
// rather than merely different: a crash between the two writes would leave the
// name gone from `list`, `get`, and the dashboard while the secret was still in
// the keychain and still injectable, so an operator revoking a leaked token
// would be told it worked and be wrong. Keychain-first leaves at worst a stale
// index entry, which the next `list` prunes.
//
// A name the keychain does not hold still has its index entry removed, and
// that case returns ErrStaleIndexEntry. A name held by neither returns nil, so
// deleting twice is not an error.
func (s *Store) Delete(name string) error {
	err := s.keychain.Delete(name)
	missing := errors.Is(err, ErrNotFound)
	if err != nil && !missing {
		return err
	}

	names, err := s.index.Names()
	if err != nil {
		return err
	}
	indexed := slices.Contains(names, name)
	if indexed {
		if err := s.index.Remove(name); err != nil {
			return err
		}
	}

	if missing && indexed {
		return fmt.Errorf("%w: %q", ErrStaleIndexEntry, name)
	}
	return nil
}

// Rebind re-scopes an existing credential to a new host list, returning the
// hosts it was bound to before.
//
// Concurrency: this is the only operation that reads the keychain value and
// writes it back, so a `credential set` that rotates the same name while a
// rebind is in flight can be overwritten by the rebind's write-back of the old
// value, with both commands reporting success. Never run `set` and `rebind` on
// the same name concurrently. Solving it would need a version field in the
// stored envelope, which is a format change for a race between two commands
// one person runs.
func (s *Store) Rebind(name string, hosts []string) ([]string, error) {
	// Fail before touching the keychain when the index cannot be read: a
	// rebind that succeeds and then reports an index error leaves the operator
	// unsure whether the binding changed.
	if _, err := s.index.Names(); err != nil {
		return nil, err
	}

	record, err := s.keychain.Get(name)
	if err != nil {
		if errors.Is(err, ErrNotFound) && s.EnvManaged(name) {
			// Only when env is the source that would actually win. A keychain
			// entry shadowing an env one is rebound normally below, because
			// that is the value the proxy injects — pointing the operator at
			// config.json there would send them to edit a file with no effect
			// on the live binding.
			return nil, fmt.Errorf("%w: %q", ErrEnvManaged, name)
		}
		return nil, err
	}

	normalized, err := NormalizeHosts(hosts)
	if err != nil {
		return nil, err
	}
	if err := s.keychain.Set(name, record.Value, normalized); err != nil {
		return nil, err
	}
	if err := s.index.Add(name); err != nil {
		return record.Hosts, fmt.Errorf("%w: %w", ErrIndexNotUpdated, err)
	}
	return record.Hosts, nil
}

// Describe returns metadata for one credential, never its value.
//
// It replicates Resolver.lookup: the keychain is consulted first, and any
// keychain failure — including ErrUnavailable, not only ErrNotFound — falls
// through to env_credentials. That is what the running proxy does, so a name
// backed by both keeps resolving through env while the keychain is locked, and
// `get` has to report the source that would actually be used rather than the
// one that failed.
//
// A keychain hit re-registers the name in the index, which is what makes index
// loss recoverable.
func (s *Store) Describe(name string) (Metadata, error) {
	var unavailable error

	meta, err := s.keychain.Describe(name)
	switch {
	case err == nil:
		if err := s.index.Add(name); err != nil {
			return Metadata{}, err
		}
		return meta, nil
	case errors.Is(err, ErrNotFound):
		// Fall through to env.
	default:
		unavailable = err
	}

	if s.env != nil {
		meta, err := s.env.Describe(name)
		switch {
		case err == nil:
			return meta, nil
		case errors.Is(err, ErrNotFound):
			// Fall through to the not-found result below.
		default:
			if unavailable == nil {
				unavailable = err
			}
		}
	}

	if unavailable != nil {
		return Metadata{}, unavailable
	}
	return Metadata{}, fmt.Errorf("%w: %q", ErrNotFound, name)
}

// List returns one row per indexed credential plus one per env_credentials
// entry, and prunes index entries whose keychain item is gone.
//
// An unreachable keychain fails the whole listing rather than returning the
// rows that happened to resolve: a partial list reads as a complete one.
// Nothing is pruned in that case — treating "could not ask" as "not there"
// would delete an operator's index over a locked keychain.
func (s *Store) List() (Listing, error) {
	indexed, err := s.index.Names()
	if err != nil {
		return Listing{}, err
	}

	rows := make([]Metadata, 0, len(indexed))
	survivors := make([]string, 0, len(indexed))
	var pruned []string
	fromKeychain := make(map[string]bool, len(indexed))
	listed := make(map[string]bool, len(indexed))

	for _, name := range indexed {
		meta, err := s.Describe(name)
		switch {
		case err == nil:
			survivors = append(survivors, name)
			rows = append(rows, meta)
			listed[name] = true
			fromKeychain[name] = meta.Source == SourceKeychain
		case errors.Is(err, ErrNotFound):
			pruned = append(pruned, name)
		default:
			return Listing{}, fmt.Errorf("%w%s", err, listUnavailableCaveat())
		}
	}

	if len(pruned) > 0 {
		if err := s.index.Keep(survivors); err != nil {
			return Listing{}, err
		}
	}

	if s.env != nil {
		for _, name := range s.env.Names() {
			// A keychain entry of the same name shadows this one, and the
			// shadowed row is not shown twice.
			if source, _ := SourceFor(fromKeychain[name], true); source == SourceKeychain {
				continue
			}
			if listed[name] {
				continue
			}
			meta, err := s.env.Describe(name)
			if err != nil {
				// A spec that cannot produce metadata — a host list that no
				// longer normalises, say — is shown without hosts rather than
				// dropped, so a broken entry is visible.
				meta = Metadata{Name: name, Source: SourceEnv}
			}
			rows = append(rows, meta)
			listed[name] = true
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return Listing{Credentials: rows, Pruned: pruned}, nil
}

// listUnavailableCaveat appends what a failed listing does and does not mean.
//
// Without it the natural reading of a failed `list` is that credentials have
// stopped working, which would send an operator restarting a proxy that is
// still injecting fine through env_credentials.
func listUnavailableCaveat() string {
	return "\n" +
		"  Nothing was pruned from the credential index. A name with an env_credentials\n" +
		"  fallback may still be injecting, so a failed list is not evidence that the\n" +
		"  proxy has stopped serving requests."
}

// EnvManaged reports whether env_credentials configures this name.
//
// `credential get` needs it to warn that a keychain entry is shadowing an env
// one. It answers only "does env hold this name?" — the precedence decision
// itself stays in SourceFor, which the caller passes this to.
func (s *Store) EnvManaged(name string) bool {
	if s.env == nil {
		return false
	}
	return slices.Contains(s.env.Names(), name)
}
