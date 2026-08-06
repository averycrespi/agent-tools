package credentials_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/credentials"
)

// fakeSecret is stored by the fake keychain so any test can assert that no
// error, message, or listing ever carries a value.
const fakeSecret = "SECRET-DO-NOT-PRINT"

// fakeKeychain is an in-memory KeychainSource with per-method error injection,
// so one test can make Describe unavailable while Get still works.
type fakeKeychain struct {
	items map[string]credentials.Record
	errs  map[string]error
	// observe runs at the top of each mutating call, before the fake changes
	// anything. Tests use it to snapshot the index and assert write ordering.
	observe func(op string)
}

func newFakeKeychain() *fakeKeychain {
	return &fakeKeychain{
		items: make(map[string]credentials.Record),
		errs:  make(map[string]error),
	}
}

func (f *fakeKeychain) fail(op string) error { return f.errs[op] }

func (f *fakeKeychain) Get(name string) (credentials.Record, error) {
	if err := f.fail("Get"); err != nil {
		return credentials.Record{}, err
	}
	record, ok := f.items[name]
	if !ok {
		return credentials.Record{}, fmt.Errorf("%w: %q", credentials.ErrNotFound, name)
	}
	return record, nil
}

func (f *fakeKeychain) Set(name, value string, hosts []string) error {
	if f.observe != nil {
		f.observe("Set")
	}
	if err := f.fail("Set"); err != nil {
		return err
	}
	f.items[name] = credentials.Record{Value: value, Hosts: hosts}
	return nil
}

func (f *fakeKeychain) Delete(name string) error {
	if f.observe != nil {
		f.observe("Delete")
	}
	if err := f.fail("Delete"); err != nil {
		return err
	}
	if _, ok := f.items[name]; !ok {
		return fmt.Errorf("%w: %q", credentials.ErrNotFound, name)
	}
	delete(f.items, name)
	return nil
}

func (f *fakeKeychain) Describe(name string) (credentials.Metadata, error) {
	if err := f.fail("Describe"); err != nil {
		return credentials.Metadata{}, err
	}
	record, err := f.Get(name)
	if err != nil {
		return credentials.Metadata{}, err
	}
	return credentials.Metadata{
		Name:   name,
		Source: credentials.SourceKeychain,
		Hosts:  record.Hosts,
		Bytes:  len(record.Value),
	}, nil
}

// newTestStore returns a Store over a fake keychain and an index in a fresh
// temp directory. No test in this file may reach a real keychain.
func newTestStore(t *testing.T, env *credentials.Env) (*credentials.Store, *fakeKeychain, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	k := newFakeKeychain()
	return credentials.NewStoreWith(k, credentials.NewIndexAt(path), env), k, path
}

func indexNames(t *testing.T, path string) []string {
	t.Helper()
	names, err := credentials.NewIndexAt(path).Names()
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}
	return names
}

func TestStore(t *testing.T) {
	t.Run("set_indexes_after_keychain", func(t *testing.T) {
		store, k, path := newTestStore(t, nil)

		var atKeychainWrite []string
		k.observe = func(string) { atKeychainWrite = indexNames(t, path) }

		if err := store.Set("gh_bot", fakeSecret, []string{"api.github.com"}); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if len(atKeychainWrite) != 0 {
			t.Fatalf("the index held %v when the keychain was written; the keychain must go first", atKeychainWrite)
		}
		if got := indexNames(t, path); !reflect.DeepEqual(got, []string{"gh_bot"}) {
			t.Fatalf("index = %v, want [gh_bot]", got)
		}

		// A failed keychain write leaves the index untouched, and its sentinel
		// survives the trip through Store.
		k.observe = nil
		k.errs["Set"] = fmt.Errorf("%w: locked", credentials.ErrUnavailable)
		err := store.Set("svc_api", fakeSecret, []string{"api.stripe.com"})
		if !errors.Is(err, credentials.ErrUnavailable) {
			t.Fatalf("Set error = %v, want it to wrap ErrUnavailable", err)
		}
		if got := indexNames(t, path); !reflect.DeepEqual(got, []string{"gh_bot"}) {
			t.Fatalf("a failed keychain write changed the index to %v", got)
		}
	})

	t.Run("set_index_failure_keeps_credential", func(t *testing.T) {
		// A directory where the index file belongs: atomicfile's rename fails,
		// which is the cheapest injectable index failure.
		dir := t.TempDir()
		path := filepath.Join(dir, "credentials.json")
		if err := os.Mkdir(path, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		k := newFakeKeychain()
		store := credentials.NewStoreWith(k, credentials.NewIndexAt(path), nil)

		err := store.Set("gh_bot", fakeSecret, []string{"api.github.com"})
		if !errors.Is(err, credentials.ErrIndexNotUpdated) {
			t.Fatalf("Set error = %v, want it to wrap ErrIndexNotUpdated", err)
		}
		if strings.Contains(err.Error(), fakeSecret) {
			t.Fatalf("the error carries the credential value: %q", err)
		}
		// The credential itself is stored and still retrievable.
		if _, getErr := k.Get("gh_bot"); getErr != nil {
			t.Fatalf("the credential was not stored: %v", getErr)
		}
	})

	t.Run("delete_writes_keychain_before_index", func(t *testing.T) {
		store, k, path := newTestStore(t, nil)
		if err := store.Set("gh_bot", fakeSecret, []string{"api.github.com"}); err != nil {
			t.Fatalf("Set: %v", err)
		}

		var atKeychainDelete []string
		k.observe = func(string) { atKeychainDelete = indexNames(t, path) }

		if err := store.Delete("gh_bot"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if !reflect.DeepEqual(atKeychainDelete, []string{"gh_bot"}) {
			t.Fatalf("the index held %v when the keychain item was deleted; the index must go second",
				atKeychainDelete)
		}
		if got := indexNames(t, path); len(got) != 0 {
			t.Fatalf("index = %v, want empty after Delete", got)
		}
	})

	t.Run("delete_stale_index_entry", func(t *testing.T) {
		store, _, path := newTestStore(t, nil)
		// Indexed but never stored: the state a crash between the two writes,
		// or a manual keychain deletion, leaves behind.
		if err := credentials.NewIndexAt(path).Add("ghost"); err != nil {
			t.Fatalf("seeding the index: %v", err)
		}

		err := store.Delete("ghost")
		if !errors.Is(err, credentials.ErrStaleIndexEntry) {
			t.Fatalf("Delete error = %v, want it to wrap ErrStaleIndexEntry", err)
		}
		if got := indexNames(t, path); len(got) != 0 {
			t.Fatalf("index = %v, want the stale entry removed", got)
		}

		// Held by neither store: deleting twice is not an error, and is not
		// reported as a stale cleanup either.
		if err := store.Delete("ghost"); err != nil {
			t.Fatalf("deleting an unknown name = %v, want nil", err)
		}
	})

	t.Run("rebind_preserves_value", func(t *testing.T) {
		store, k, path := newTestStore(t, nil)
		if err := store.Set("gh_bot", fakeSecret, []string{"api.github.com"}); err != nil {
			t.Fatalf("Set: %v", err)
		}

		old, err := store.Rebind("gh_bot", []string{"uploads.github.com"})
		if err != nil {
			t.Fatalf("Rebind: %v", err)
		}
		if want := []string{"api.github.com"}; !reflect.DeepEqual(old, want) {
			t.Fatalf("Rebind returned old hosts %v, want %v", old, want)
		}

		record, err := k.Get("gh_bot")
		if err != nil {
			t.Fatalf("Get after Rebind: %v", err)
		}
		if record.Value != fakeSecret {
			t.Fatal("Rebind changed the stored value")
		}
		if want := []string{"uploads.github.com"}; !reflect.DeepEqual(record.Hosts, want) {
			t.Fatalf("hosts after Rebind = %v, want %v", record.Hosts, want)
		}
		if got := indexNames(t, path); !reflect.DeepEqual(got, []string{"gh_bot"}) {
			t.Fatalf("index = %v, want [gh_bot]", got)
		}

		// A name with no keychain item is ErrNotFound, and writes nothing.
		if _, err := store.Rebind("absent", []string{"example.com"}); !errors.Is(err, credentials.ErrNotFound) {
			t.Fatalf("Rebind of an absent name = %v, want ErrNotFound", err)
		}
		if got := indexNames(t, path); !reflect.DeepEqual(got, []string{"gh_bot"}) {
			t.Fatalf("a failed Rebind changed the index to %v", got)
		}
	})

	t.Run("rebind_shadowed_by_keychain", func(t *testing.T) {
		t.Setenv("SHADOWED_TOKEN", "ENV-VALUE")
		env := credentials.NewEnv(map[string]credentials.EnvSpec{
			"gh_bot":  {Var: "SHADOWED_TOKEN", Hosts: []string{"api.github.com"}},
			"env_own": {Var: "SHADOWED_TOKEN", Hosts: []string{"api.stripe.com"}},
		})
		store, k, _ := newTestStore(t, env)
		if err := store.Set("gh_bot", fakeSecret, []string{"api.github.com"}); err != nil {
			t.Fatalf("Set: %v", err)
		}

		// The keychain entry is the one the proxy injects, so it is the one
		// rebind must change — not an ErrEnvManaged refusal.
		if _, err := store.Rebind("gh_bot", []string{"uploads.github.com"}); err != nil {
			t.Fatalf("Rebind of a keychain entry shadowing an env one = %v, want success", err)
		}
		record, err := k.Get("gh_bot")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if want := []string{"uploads.github.com"}; !reflect.DeepEqual(record.Hosts, want) {
			t.Fatalf("hosts after Rebind = %v, want %v", record.Hosts, want)
		}

		// A name only env_credentials holds is refused: config.json owns it.
		_, err = store.Rebind("env_own", []string{"api.stripe.com"})
		if !errors.Is(err, credentials.ErrEnvManaged) {
			t.Fatalf("Rebind of an env-only name = %v, want ErrEnvManaged", err)
		}
	})

	t.Run("describe_falls_through_to_env_when_unavailable", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "ENV-VALUE")
		env := credentials.NewEnv(map[string]credentials.EnvSpec{
			"gh_bot": {Var: "GH_TOKEN", Hosts: []string{"api.github.com"}},
		})
		store, k, path := newTestStore(t, env)

		// A locked keychain, not a missing item. The proxy still resolves this
		// name through env, so Describe must report the env source.
		k.errs["Describe"] = fmt.Errorf("%w: locked", credentials.ErrUnavailable)
		meta, err := store.Describe("gh_bot")
		if err != nil {
			t.Fatalf("Describe: %v", err)
		}
		if meta.Source != credentials.SourceEnv {
			t.Fatalf("source = %q, want %q", meta.Source, credentials.SourceEnv)
		}

		// A name only the keychain would hold, with the keychain unavailable,
		// stays unavailable rather than becoming not-found.
		if _, err := store.Describe("keychain_only"); !errors.Is(err, credentials.ErrUnavailable) {
			t.Fatalf("Describe of an uncovered name = %v, want ErrUnavailable", err)
		}

		// A keychain hit re-registers the name, which is what makes index loss
		// recoverable.
		delete(k.errs, "Describe")
		if err := k.Set("svc_api", fakeSecret, []string{"api.stripe.com"}); err != nil {
			t.Fatalf("seeding the keychain: %v", err)
		}
		if _, err := store.Describe("svc_api"); err != nil {
			t.Fatalf("Describe: %v", err)
		}
		if got := indexNames(t, path); !reflect.DeepEqual(got, []string{"svc_api"}) {
			t.Fatalf("index = %v, want [svc_api] after a keychain hit", got)
		}

		// Neither source holds it.
		if _, err := store.Describe("nowhere"); !errors.Is(err, credentials.ErrNotFound) {
			t.Fatalf("Describe of an unknown name = %v, want ErrNotFound", err)
		}
	})

	t.Run("list_prunes_not_found", func(t *testing.T) {
		store, _, path := newTestStore(t, nil)
		if err := store.Set("gh_bot", fakeSecret, []string{"api.github.com"}); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := credentials.NewIndexAt(path).Add("ghost"); err != nil {
			t.Fatalf("seeding a stale entry: %v", err)
		}

		listing, err := store.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(listing.Credentials) != 1 || listing.Credentials[0].Name != "gh_bot" {
			t.Fatalf("rows = %+v, want only gh_bot", listing.Credentials)
		}
		if !reflect.DeepEqual(listing.Pruned, []string{"ghost"}) {
			t.Fatalf("Pruned = %v, want [ghost]", listing.Pruned)
		}
		if got := indexNames(t, path); !reflect.DeepEqual(got, []string{"gh_bot"}) {
			t.Fatalf("index = %v, want the stale entry pruned", got)
		}
	})

	t.Run("list_env_rows_and_shadowing", func(t *testing.T) {
		t.Setenv("SHARED_TOKEN", "ENV-VALUE")
		env := credentials.NewEnv(map[string]credentials.EnvSpec{
			"gh_bot":  {Var: "SHARED_TOKEN", Hosts: []string{"api.github.com"}},
			"env_own": {Var: "SHARED_TOKEN", Hosts: []string{"api.stripe.com"}},
		})
		store, _, path := newTestStore(t, env)
		if err := store.Set("gh_bot", fakeSecret, []string{"api.github.com"}); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := store.Set("svc_api", fakeSecret, []string{"api.stripe.com"}); err != nil {
			t.Fatalf("Set: %v", err)
		}

		listing, err := store.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		var got []string
		for _, row := range listing.Credentials {
			got = append(got, row.Name+"="+row.Source)
		}
		want := []string{
			"env_own=" + credentials.SourceEnv,
			"gh_bot=" + credentials.SourceKeychain,
			"svc_api=" + credentials.SourceKeychain,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %v, want %v (sorted by name, no shadowed env row)", got, want)
		}
		// env rows are never indexed.
		if idx := indexNames(t, path); !reflect.DeepEqual(idx, []string{"gh_bot", "svc_api"}) {
			t.Fatalf("index = %v, want env names left out", idx)
		}
	})

	t.Run("no_method_returns_a_value_in_an_error", func(t *testing.T) {
		store, k, _ := newTestStore(t, nil)
		if err := store.Set("gh_bot", fakeSecret, []string{"api.github.com"}); err != nil {
			t.Fatalf("Set: %v", err)
		}

		// Force every method down an error path with the value in the store.
		k.errs["Get"] = fmt.Errorf("%w: locked", credentials.ErrUnavailable)
		k.errs["Set"] = fmt.Errorf("%w: locked", credentials.ErrUnavailable)
		k.errs["Delete"] = fmt.Errorf("%w: locked", credentials.ErrUnavailable)
		k.errs["Describe"] = fmt.Errorf("%w: locked", credentials.ErrUnavailable)

		var errs []error
		errs = append(errs, store.Set("gh_bot", fakeSecret, []string{"api.github.com"}))
		errs = append(errs, store.Delete("gh_bot"))
		_, rebindErr := store.Rebind("gh_bot", []string{"api.github.com"})
		errs = append(errs, rebindErr)
		_, describeErr := store.Describe("gh_bot")
		errs = append(errs, describeErr)
		_, listErr := store.List()
		errs = append(errs, listErr)

		for _, err := range errs {
			if err != nil && strings.Contains(err.Error(), fakeSecret) {
				t.Fatalf("an error carries the credential value: %q", err)
			}
		}
	})
}

// TestStoreListKeychainUnavailable is the destructive-mistake guard: an
// unreachable keychain must never be read as "the credential is gone", because
// pruning on it deletes operator state that cannot be rebuilt from the
// keychain.
func TestStoreListKeychainUnavailable(t *testing.T) {
	store, k, path := newTestStore(t, nil)
	if err := store.Set("gh_bot", fakeSecret, []string{"api.github.com"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := credentials.NewIndexAt(path).Add("svc_api"); err != nil {
		t.Fatalf("seeding the index: %v", err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}

	k.errs["Describe"] = fmt.Errorf("%w: the keychain is locked", credentials.ErrUnavailable)
	_, err = store.List()
	if !errors.Is(err, credentials.ErrUnavailable) {
		t.Fatalf("List error = %v, want it to wrap ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "env_credentials") {
		t.Fatalf("error %q should say a name with an env_credentials fallback may still be injecting", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("the index changed from %s to %s; ErrUnavailable must never prune", before, after)
	}
}
