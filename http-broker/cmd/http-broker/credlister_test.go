package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/config"
	"github.com/averycrespi/agent-tools/http-broker/internal/credentials"
	"github.com/averycrespi/agent-tools/http-broker/internal/dashboard"
)

// describeFrom builds a describe function over an in-memory keychain, so these
// tests never touch a real one.
func describeFrom(items map[string][]string) func(string) (credentials.Metadata, error) {
	return func(name string) (credentials.Metadata, error) {
		hosts, ok := items[name]
		if !ok {
			return credentials.Metadata{}, fmt.Errorf("%w: %q", credentials.ErrNotFound, name)
		}
		return credentials.Metadata{
			Name:   name,
			Source: credentials.SourceKeychain,
			Hosts:  hosts,
			Bytes:  len(cliSecret),
		}, nil
	}
}

func names(infos []dashboard.CredentialInfo) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}

func infoFor(t *testing.T, infos []dashboard.CredentialInfo, name string) dashboard.CredentialInfo {
	t.Helper()
	for _, info := range infos {
		if info.Name == name {
			return info
		}
	}
	t.Fatalf("no row for %q in %+v", name, infos)
	return dashboard.CredentialInfo{}
}

// TestCredentialListerNeverUsesTheStore is the portable half of the read-only
// guard. TestDashboardReadOnly hashes the index across a route sweep, but that
// only catches a mutating describe where a keychain exists: Store.Describe
// writes on a keychain hit, and a host with no Secret Service answers every
// lookup with ErrUnavailable, which writes nothing. This check runs anywhere.
func TestCredentialListerNeverUsesTheStore(t *testing.T) {
	// The AST, not the file text: a comment naming the type is fine — this
	// file and credlister.go both explain the rule — and `rules.Store` in
	// serve.go is a different type entirely.
	banned := map[string]bool{"Store": true, "NewStore": true, "NewStoreWith": true}

	for _, path := range []string{"credlister.go", "serve.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "credentials" || !banned[sel.Sel.Name] {
				return true
			}
			t.Errorf("%s uses credentials.%s; the dashboard must describe through the bare Keychain, "+
				"because Store.Describe re-registers the name in the index", path, sel.Sel.Name)
			return true
		})
	}
}

func TestCredentialInfos(t *testing.T) {
	t.Run("union", func(t *testing.T) {
		infos := credentialInfos(
			[]string{"indexed_only", "shared"},
			[]string{"referenced_only", "shared"},
			map[string]config.EnvCredential{
				"env_only": {Var: "TOK", Hosts: []string{"api.stripe.com"}},
			},
			describeFrom(map[string][]string{
				"indexed_only": {"api.github.com"},
				"shared":       {"api.github.com"},
			}),
		)

		want := []string{"env_only", "indexed_only", "referenced_only", "shared"}
		if got := names(infos); !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %v, want %v (sorted, no duplicates)", got, want)
		}
	})

	t.Run("keychain_precedence", func(t *testing.T) {
		infos := credentialInfos(
			[]string{"both"},
			nil,
			map[string]config.EnvCredential{
				"both": {Var: "TOK", Hosts: []string{"api.stripe.com"}},
			},
			describeFrom(map[string][]string{"both": {"api.github.com"}}),
		)

		// The proxy resolves keychain-first, so a row for a name held by both
		// must report the keychain and its hosts. Reporting env_credentials
		// here would name a source the request never uses.
		row := infoFor(t, infos, "both")
		if row.Source != credentials.SourceKeychain {
			t.Fatalf("source = %q, want %q", row.Source, credentials.SourceKeychain)
		}
		if want := []string{"api.github.com"}; !reflect.DeepEqual(row.Hosts, want) {
			t.Fatalf("hosts = %v, want the keychain's %v", row.Hosts, want)
		}
		if len(infos) != 1 {
			t.Fatalf("rows = %+v, want one row for a name held by both sources", infos)
		}
	})

	t.Run("referenced_true_and_false", func(t *testing.T) {
		infos := credentialInfos(
			[]string{"unused", "used"},
			[]string{"used"},
			nil,
			describeFrom(map[string][]string{
				"unused": {"api.github.com"},
				"used":   {"api.github.com"},
			}),
		)

		if row := infoFor(t, infos, "used"); !row.Referenced {
			t.Fatal("a name a rule references should be Referenced")
		}
		if row := infoFor(t, infos, "unused"); row.Referenced {
			t.Fatal("a stored name no rule references should not be Referenced")
		}
	})

	t.Run("describe_error_shows_unavailable", func(t *testing.T) {
		infos := credentialInfos(
			nil,
			[]string{"missing"},
			nil,
			func(string) (credentials.Metadata, error) {
				return credentials.Metadata{}, fmt.Errorf("%w: locked", credentials.ErrUnavailable)
			},
		)

		row := infoFor(t, infos, "missing")
		if row.Source != "keychain (unavailable)" {
			t.Fatalf("source = %q, want %q", row.Source, "keychain (unavailable)")
		}
		if len(row.Hosts) != 0 {
			t.Fatalf("hosts = %v, want none for an unavailable item", row.Hosts)
		}
		if !row.Referenced {
			t.Fatal("a referenced-but-missing credential is the case worth seeing; it must stay Referenced")
		}
	})

	t.Run("index_read_failure_sets_index_error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
			t.Fatalf("writing a corrupt index: %v", err)
		}

		listing := credentialListing(
			credentials.NewIndexAt(path),
			[]string{"referenced"},
			map[string]config.EnvCredential{
				"env_only": {Var: "TOK", Hosts: []string{"api.stripe.com"}},
			},
			describeFrom(nil),
		)

		if listing.IndexError == "" {
			t.Fatal("a corrupt index must set IndexError rather than silently dropping rows")
		}
		if !strings.Contains(listing.IndexError, path) {
			t.Fatalf("IndexError = %q, want it to name the path", listing.IndexError)
		}
		if strings.Contains(listing.IndexError, cliSecret) {
			t.Fatalf("IndexError carries a credential value: %q", listing.IndexError)
		}
		// The rows that do not depend on the index still render.
		want := []string{"env_only", "referenced"}
		if got := names(listing.Credentials); !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %v, want the referenced and env rows %v", got, want)
		}
	})

	t.Run("a_readable_index_sets_no_error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := credentials.NewIndexAt(path).Add("gh_bot"); err != nil {
			t.Fatalf("seeding the index: %v", err)
		}

		listing := credentialListing(
			credentials.NewIndexAt(path),
			nil,
			nil,
			describeFrom(map[string][]string{"gh_bot": {"api.github.com"}}),
		)

		if listing.IndexError != "" {
			t.Fatalf("IndexError = %q, want empty", listing.IndexError)
		}
		if got := names(listing.Credentials); !reflect.DeepEqual(got, []string{"gh_bot"}) {
			t.Fatalf("rows = %v, want [gh_bot]", got)
		}
	})
}
