package credentials_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/credentials"
)

// indexPath returns a path inside a fresh temp directory. The parent of the
// index file is deliberately left uncreated in some cases, to exercise AC-1.7.
func indexPath(t *testing.T, elem ...string) string {
	t.Helper()
	return filepath.Join(append([]string{t.TempDir()}, elem...)...)
}

func TestIndex(t *testing.T) {
	t.Run("absent_file_is_empty_not_an_error", func(t *testing.T) {
		idx := credentials.NewIndexAt(indexPath(t, "credentials.json"))

		names, err := idx.Names()
		if err != nil {
			t.Fatalf("Names() on an absent file returned %v, want nil", err)
		}
		if len(names) != 0 {
			t.Fatalf("Names() = %v, want empty", names)
		}
	})

	t.Run("round_trip_persists_without_a_save_step", func(t *testing.T) {
		path := indexPath(t, "credentials.json")

		if err := credentials.NewIndexAt(path).Add("svc_api"); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if err := credentials.NewIndexAt(path).Add("gh_bot"); err != nil {
			t.Fatalf("Add: %v", err)
		}

		// A fresh value over the same path sees both names: nothing is cached
		// in memory, and no separate save call exists.
		names, err := credentials.NewIndexAt(path).Names()
		if err != nil {
			t.Fatalf("Names: %v", err)
		}
		if want := []string{"gh_bot", "svc_api"}; !reflect.DeepEqual(names, want) {
			t.Fatalf("Names() = %v, want %v", names, want)
		}

		if err := credentials.NewIndexAt(path).Remove("svc_api"); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		names, err = credentials.NewIndexAt(path).Names()
		if err != nil {
			t.Fatalf("Names: %v", err)
		}
		if want := []string{"gh_bot"}; !reflect.DeepEqual(names, want) {
			t.Fatalf("after Remove, Names() = %v, want %v", names, want)
		}
	})

	t.Run("keep_retains_only_the_given_names", func(t *testing.T) {
		path := indexPath(t, "credentials.json")
		idx := credentials.NewIndexAt(path)
		for _, name := range []string{"a", "b", "c"} {
			if err := idx.Add(name); err != nil {
				t.Fatalf("Add(%q): %v", name, err)
			}
		}

		// "d" is not indexed, so Keep must not add it: Keep only ever prunes.
		if err := idx.Keep([]string{"a", "c", "d"}); err != nil {
			t.Fatalf("Keep: %v", err)
		}
		names, err := idx.Names()
		if err != nil {
			t.Fatalf("Names: %v", err)
		}
		if want := []string{"a", "c"}; !reflect.DeepEqual(names, want) {
			t.Fatalf("Names() = %v, want %v", names, want)
		}
	})

	t.Run("output_is_sorted_and_deduplicated", func(t *testing.T) {
		path := indexPath(t, "credentials.json")
		idx := credentials.NewIndexAt(path)
		for _, name := range []string{"zeta", "alpha", "mid"} {
			if err := idx.Add(name); err != nil {
				t.Fatalf("Add(%q): %v", name, err)
			}
		}

		got := readIndexFile(t, path)
		if want := `{"version":1,"names":["alpha","mid","zeta"]}`; got != want {
			t.Fatalf("on-disk index = %s, want %s", got, want)
		}

		// A duplicate on disk is collapsed on read, whatever put it there.
		writeIndexFile(t, path, `{"version":1,"names":["b","a","b"]}`)
		names, err := idx.Names()
		if err != nil {
			t.Fatalf("Names: %v", err)
		}
		if want := []string{"a", "b"}; !reflect.DeepEqual(names, want) {
			t.Fatalf("Names() = %v, want %v", names, want)
		}
	})

	t.Run("no_op_mutations_do_not_write", func(t *testing.T) {
		path := indexPath(t, "credentials.json")
		// Deliberately unsorted on disk, so any write at all is visible as a
		// byte change rather than having to compare timestamps.
		const raw = `{"version":1,"names":["zeta","alpha"]}`
		writeIndexFile(t, path, raw)
		idx := credentials.NewIndexAt(path)

		if err := idx.Add("alpha"); err != nil {
			t.Fatalf("Add of a present name: %v", err)
		}
		if got := readIndexFile(t, path); got != raw {
			t.Fatalf("Add of a present name rewrote the file: %s", got)
		}

		if err := idx.Remove("absent"); err != nil {
			t.Fatalf("Remove of an absent name: %v", err)
		}
		if got := readIndexFile(t, path); got != raw {
			t.Fatalf("Remove of an absent name rewrote the file: %s", got)
		}

		if err := idx.Keep([]string{"alpha", "zeta"}); err != nil {
			t.Fatalf("Keep of the current set: %v", err)
		}
		if got := readIndexFile(t, path); got != raw {
			t.Fatalf("Keep of the current set rewrote the file: %s", got)
		}
	})

	t.Run("malformed_json_errors_without_truncating", func(t *testing.T) {
		path := indexPath(t, "credentials.json")
		const raw = `not json at all`
		writeIndexFile(t, path, raw)
		idx := credentials.NewIndexAt(path)

		_, err := idx.Names()
		if err == nil {
			t.Fatal("Names() on malformed JSON returned nil error")
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("error %q does not name the file path %q", err, path)
		}
		if !strings.Contains(err.Error(), "deleting the file is safe") {
			t.Fatalf("error %q does not say deleting the file is safe", err)
		}
		if got := readIndexFile(t, path); got != raw {
			t.Fatalf("the file was rewritten to %s", got)
		}

		// Mutators surface the same error rather than overwriting the file.
		if err := idx.Add("gh_bot"); err == nil {
			t.Fatal("Add over a malformed index returned nil error")
		}
		if got := readIndexFile(t, path); got != raw {
			t.Fatalf("Add over a malformed index rewrote the file: %s", got)
		}
	})

	t.Run("unknown_version_errors_without_truncating", func(t *testing.T) {
		path := indexPath(t, "credentials.json")
		const raw = `{"version":2,"names":["gh_bot"]}`
		writeIndexFile(t, path, raw)

		_, err := credentials.NewIndexAt(path).Names()
		if err == nil {
			t.Fatal("Names() on an unknown version returned nil error")
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("error %q does not name the file path %q", err, path)
		}
		if !strings.Contains(err.Error(), "version 2") {
			t.Fatalf("error %q does not name the unrecognised version", err)
		}
		if got := readIndexFile(t, path); got != raw {
			t.Fatalf("the file was rewritten to %s", got)
		}
	})

	t.Run("invalid_name_is_rejected_without_writing", func(t *testing.T) {
		path := indexPath(t, "credentials.json")
		idx := credentials.NewIndexAt(path)

		err := idx.Add("bad name!")
		if err == nil {
			t.Fatal("Add of an invalid name returned nil error")
		}
		if !strings.Contains(err.Error(), ". _ -") {
			t.Fatalf("error %q does not name the allowed characters", err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("Add of an invalid name created %s", path)
		}
	})

	t.Run("file_mode_is_0600_and_parent_is_created", func(t *testing.T) {
		// A parent directory that does not exist yet, so the write has to
		// create it.
		path := indexPath(t, "nested", "dir", "credentials.json")

		if err := credentials.NewIndexAt(path).Add("gh_bot"); err != nil {
			t.Fatalf("Add: %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("index mode = %o, want 600", got)
		}
	})

	t.Run("path_reports_the_backing_file", func(t *testing.T) {
		path := indexPath(t, "credentials.json")
		if got := credentials.NewIndexAt(path).Path(); got != path {
			t.Fatalf("Path() = %q, want %q", got, path)
		}
	})
}

func readIndexFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func writeIndexFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
