package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/averycrespi/agent-tools/http-broker/internal/atomicfile"
	"github.com/averycrespi/agent-tools/http-broker/internal/paths"
)

// indexVersion is the only on-disk format this build understands. A file
// carrying anything else is refused rather than rewritten, so a newer
// http-broker's index is not silently downgraded by an older one.
const indexVersion = 1

// indexDoc is the on-disk shape: a version and a name list, nothing else.
type indexDoc struct {
	Version int      `json:"version"`
	Names   []string `json:"names"`
}

// Index is the list of credential names stored in the OS keychain.
//
// It exists because the keychain cannot be enumerated: the backend can get,
// set, and delete an item by name, but it cannot ask "what did I store?". So
// `credential list` needs a record of the names, and this file is it.
//
// It holds names only. Bound hosts stay in the keychain envelope, which
// remains the sole authority on scope — a host list duplicated here could
// disagree with the one actually enforced, and the displayed copy would be the
// convincing wrong one. Nothing here can hold a value.
//
// The index is derived state. Deleting it loses no secret, and
// `http-broker credential get <name>` re-registers a name, so a lost or
// corrupt index is recoverable rather than fatal.
//
// Concurrency: every mutator is a read-modify-write with no lock, so two
// `credential set` runs racing on the same index can drop one of the names.
// Accepted for a human-driven CLI — the credential itself is still in the
// keychain, and `credential get <name>` puts the name back.
type Index struct {
	path string
}

// NewIndex returns the index at the default data-home location.
func NewIndex() *Index { return &Index{path: paths.CredentialIndex()} }

// NewIndexAt returns the index stored at path.
func NewIndexAt(path string) *Index { return &Index{path: path} }

// Path returns the file the index is stored in.
func (i *Index) Path() string { return i.path }

// Names returns the indexed credential names, sorted and deduplicated.
//
// An absent file is not a failure: it is first run, or a wiped data home. It
// returns an empty list.
func (i *Index) Names() ([]string, error) {
	data, err := os.ReadFile(i.path)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the credential index %s: %w", i.path, err)
	}

	var doc indexDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("the credential index %s is not valid JSON: %w\n"+
			"  It holds credential names only, never values, so deleting the file is safe.\n"+
			"  Re-register a name afterwards with `http-broker credential get <name>`.", i.path, err)
	}
	if doc.Version != indexVersion {
		return nil, fmt.Errorf("the credential index %s has version %d, but this build understands version %d\n"+
			"  It holds credential names only, never values, so deleting the file is safe.\n"+
			"  Re-register a name afterwards with `http-broker credential get <name>`.",
			i.path, doc.Version, indexVersion)
	}

	return normalizeNames(doc.Names), nil
}

// Add records a credential name. It is a no-op when the name is already
// present, writing nothing at all: `credential get` re-registers on every run,
// and a health check looping over it should not churn the file.
func (i *Index) Add(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("credential name %q must contain only letters, digits, and the characters . _ -", name)
	}

	names, err := i.Names()
	if err != nil {
		return err
	}
	if slices.Contains(names, name) {
		return nil
	}
	return i.write(append(names, name))
}

// Remove drops a credential name. It is a no-op when the name is absent.
func (i *Index) Remove(name string) error {
	names, err := i.Names()
	if err != nil {
		return err
	}
	if !slices.Contains(names, name) {
		return nil
	}
	return i.write(slices.DeleteFunc(names, func(n string) bool { return n == name }))
}

// Keep retains only the given names, dropping every other entry. `list` uses
// it to prune entries whose keychain item is gone.
//
// It only ever removes: a name that is not already indexed is not added, so
// Keep cannot resurrect an entry a concurrent Remove just dropped.
func (i *Index) Keep(names []string) error {
	current, err := i.Names()
	if err != nil {
		return err
	}
	wanted := normalizeNames(names)
	retained := make([]string, 0, len(current))
	for _, name := range current {
		if slices.Contains(wanted, name) {
			retained = append(retained, name)
		}
	}
	if slices.Equal(current, retained) {
		return nil
	}
	return i.write(retained)
}

// write persists the name set, sorted and deduplicated.
func (i *Index) write(names []string) error {
	data, err := json.Marshal(indexDoc{Version: indexVersion, Names: normalizeNames(names)})
	if err != nil {
		return fmt.Errorf("encoding the credential index: %w", err)
	}
	// 0600 because the name set alone tells a reader which services this host
	// holds tokens for, even though no value is stored.
	if err := atomicfile.Write(i.path, data, 0o600); err != nil {
		return fmt.Errorf("writing the credential index %s: %w", i.path, err)
	}
	return nil
}

// normalizeNames sorts and deduplicates, so the file's byte content depends on
// the name set rather than on insertion order.
func normalizeNames(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		return []string{}
	}
	return out
}
