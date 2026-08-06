package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/credentials"
)

// cliSecret is what the fake keychain stores, so any test can assert no
// command ever prints a value.
const cliSecret = "SECRET-DO-NOT-PRINT"

// fakeKeychain is an in-memory KeychainSource with per-method error injection.
// No test in this file may reach a real keychain: Linux CI has no Secret
// Service, and a developer's machine would prompt.
type fakeKeychain struct {
	items map[string]credentials.Record
	errs  map[string]error
}

func newFakeKeychain() *fakeKeychain {
	return &fakeKeychain{
		items: make(map[string]credentials.Record),
		errs:  make(map[string]error),
	}
}

func (f *fakeKeychain) Get(name string) (credentials.Record, error) {
	if err := f.errs["Get"]; err != nil {
		return credentials.Record{}, err
	}
	record, ok := f.items[name]
	if !ok {
		return credentials.Record{}, fmt.Errorf("%w: %q", credentials.ErrNotFound, name)
	}
	return record, nil
}

func (f *fakeKeychain) Set(name, value string, hosts []string) error {
	if err := f.errs["Set"]; err != nil {
		return err
	}
	normalized, err := credentials.NormalizeHosts(hosts)
	if err != nil {
		return err
	}
	f.items[name] = credentials.Record{Value: value, Hosts: normalized}
	return nil
}

func (f *fakeKeychain) Delete(name string) error {
	if err := f.errs["Delete"]; err != nil {
		return err
	}
	if _, ok := f.items[name]; !ok {
		return fmt.Errorf("%w: %q", credentials.ErrNotFound, name)
	}
	delete(f.items, name)
	return nil
}

func (f *fakeKeychain) Describe(name string) (credentials.Metadata, error) {
	if err := f.errs["Describe"]; err != nil {
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

// credentialCLI is a fake-backed CLI fixture: the commands run for real, but
// against an injected store.
type credentialCLI struct {
	keychain  *fakeKeychain
	indexPath string
}

func newCredentialCLI(t *testing.T, env *credentials.Env) *credentialCLI {
	t.Helper()
	c := &credentialCLI{
		keychain:  newFakeKeychain(),
		indexPath: filepath.Join(t.TempDir(), "credentials.json"),
	}
	newCredentialStore = func() (*credentials.Store, error) {
		return credentials.NewStoreWith(c.keychain, credentials.NewIndexAt(c.indexPath), env), nil
	}
	t.Cleanup(func() { newCredentialStore = defaultCredentialStore })
	return c
}

// run executes one credential subcommand, returning stdout and stderr
// separately so a table piped to a file can be told apart from a warning.
func (c *credentialCLI) run(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	return runCredentialArgs(t, stdin, args...)
}

func runCredentialArgs(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()

	// Reset the repeatable --host flags. Their backing slices are appended to
	// across runs, and appending to a nil slice reproduces first-run
	// behaviour.
	credentialSetHosts = nil
	credentialRebindHosts = nil

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetIn(strings.NewReader(stdin))
	rootCmd.SetArgs(append([]string{"credential"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetIn(nil)
		rootCmd.SetArgs(nil)
	})

	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

func (c *credentialCLI) indexNames(t *testing.T) []string {
	t.Helper()
	names, err := credentials.NewIndexAt(c.indexPath).Names()
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}
	return names
}

func TestCredential(t *testing.T) {
	t.Run("set_then_list_then_rm", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)

		stdout, _, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if want := `stored "gh_bot" for api.github.com`; !strings.Contains(stdout, want) {
			t.Fatalf("set stdout = %q, want it to contain %q", stdout, want)
		}

		stdout, _, err = cli.run(t, "", "list")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(stdout, "gh_bot") {
			t.Fatalf("list stdout = %q, want it to list gh_bot", stdout)
		}
		if strings.Contains(stdout, cliSecret) {
			t.Fatalf("list printed the credential value: %q", stdout)
		}

		stdout, _, err = cli.run(t, "", "rm", "gh_bot")
		if err != nil {
			t.Fatalf("rm: %v", err)
		}
		if want := `removed "gh_bot"`; !strings.Contains(stdout, want) {
			t.Fatalf("rm stdout = %q, want it to contain %q", stdout, want)
		}
		if got := cli.indexNames(t); len(got) != 0 {
			t.Fatalf("index = %v, want empty after rm", got)
		}
	})

	t.Run("set_index_failure_warns_and_exits_zero", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)
		// A directory where the index file belongs makes the index write fail
		// while the keychain write still succeeds.
		if err := os.Mkdir(cli.indexPath, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		stdout, stderr, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com")
		if err != nil {
			t.Fatalf("set returned %v; a stored credential with an unwritten index must exit 0", err)
		}
		if want := `stored "gh_bot"`; !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want the success line %q", stdout, want)
		}
		if want := "credential get gh_bot"; !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to name %q as the repair", stderr, want)
		}
		if strings.Contains(stdout+stderr, cliSecret) {
			t.Fatalf("the value was printed: %q", stdout+stderr)
		}
	})

	t.Run("list_reports_pruned_entries_on_stderr", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)
		if _, _, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com"); err != nil {
			t.Fatalf("set: %v", err)
		}
		if err := credentials.NewIndexAt(cli.indexPath).Add("ghost"); err != nil {
			t.Fatalf("seeding a stale entry: %v", err)
		}

		stdout, stderr, err := cli.run(t, "", "list")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if want := "pruned 1 stale entry: ghost"; !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
		if strings.Contains(stdout, "pruned") {
			t.Fatalf("the prune report leaked into stdout: %q", stdout)
		}
		if strings.Contains(stdout, "ghost") {
			t.Fatalf("stdout = %q, want the pruned name gone from the table", stdout)
		}
	})

	t.Run("list_on_an_empty_index_explains_recovery", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)

		stdout, _, err := cli.run(t, "", "list")
		if err != nil {
			t.Fatalf("list on an empty index returned %v, want exit 0", err)
		}
		for _, want := range []string{
			"no credentials indexed",
			"credential set",
			"credential get <name>",
			"rules.json",
			"Credentials view",
		} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("stdout = %q, want it to mention %q", stdout, want)
			}
		}
	})

	t.Run("list_on_an_unreachable_keychain_fails_without_pruning", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)
		if _, _, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com"); err != nil {
			t.Fatalf("set: %v", err)
		}

		before, err := os.ReadFile(cli.indexPath)
		if err != nil {
			t.Fatalf("reading the index: %v", err)
		}

		// Mirrors what the real Keychain returns: ErrUnavailable carrying the
		// remediation text.
		cli.keychain.errs["Describe"] = fmt.Errorf("%w: reading %q from the keychain\n%s",
			credentials.ErrUnavailable, "gh_bot", "  The OS keychain could not be reached.")

		_, _, err = cli.run(t, "", "list")
		if err == nil {
			t.Fatal("list on an unreachable keychain returned nil; it must exit 1")
		}
		if !strings.Contains(err.Error(), "The OS keychain could not be reached") {
			t.Fatalf("error %q drops the keychain remediation text", err)
		}
		if !strings.Contains(err.Error(), "env_credentials") {
			t.Fatalf("error %q should caveat that an env_credentials fallback may still be injecting", err)
		}

		after, readErr := os.ReadFile(cli.indexPath)
		if readErr != nil {
			t.Fatalf("reading the index: %v", readErr)
		}
		if string(before) != string(after) {
			t.Fatalf("the index changed from %s to %s; an unreachable keychain must never prune", before, after)
		}
	})

	t.Run("get_unknown_name_names_both_sources", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)

		_, _, err := cli.run(t, "", "get", "nowhere")
		if err == nil {
			t.Fatal("get of an unknown name returned nil; it must exit 1")
		}
		for _, want := range []string{"keychain", "config.json"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q should name %q", err, want)
			}
		}
	})

	t.Run("get_warns_when_a_keychain_entry_shadows_an_env_one", func(t *testing.T) {
		t.Setenv("SHADOWED_TOKEN", "ENV-VALUE")
		env := credentials.NewEnv(map[string]credentials.EnvSpec{
			"gh_bot": {Var: "SHADOWED_TOKEN", Hosts: []string{"api.github.com"}},
		})
		cli := newCredentialCLI(t, env)
		if _, _, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com"); err != nil {
			t.Fatalf("set: %v", err)
		}

		stdout, stderr, err := cli.run(t, "", "get", "gh_bot")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !strings.Contains(stdout, credentials.SourceKeychain) {
			t.Fatalf("stdout = %q, want the keychain row", stdout)
		}
		if !strings.Contains(stderr, "shadowed") {
			t.Fatalf("stderr = %q, want a shadowing warning", stderr)
		}
		if strings.Contains(stdout+stderr, cliSecret) {
			t.Fatalf("the value was printed: %q", stdout+stderr)
		}
	})

	t.Run("rebind_reports_old_and_new_hosts", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)
		if _, _, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com"); err != nil {
			t.Fatalf("set: %v", err)
		}

		stdout, _, err := cli.run(t, "", "rebind", "gh_bot", "--host", "uploads.github.com")
		if err != nil {
			t.Fatalf("rebind: %v", err)
		}
		if want := `rebound "gh_bot": api.github.com -> uploads.github.com`; !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want it to contain %q", stdout, want)
		}
		for _, want := range []string{"30s", "kill -HUP"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("stdout = %q, want it to mention %q", stdout, want)
			}
		}
		if record, getErr := cli.keychain.Get("gh_bot"); getErr != nil || record.Value != cliSecret {
			t.Fatalf("rebind did not preserve the value: %+v, %v", record, getErr)
		}
	})

	t.Run("rebind_requires_a_host_and_rejects_wildcards", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)
		if _, _, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com"); err != nil {
			t.Fatalf("set: %v", err)
		}

		if _, _, err := cli.run(t, "", "rebind", "gh_bot"); err == nil {
			t.Fatal("rebind with no --host returned nil; at least one is required")
		}
		for _, host := range []string{"*", "**", "*.com"} {
			if _, _, err := cli.run(t, "", "rebind", "gh_bot", "--host", host); err == nil {
				t.Fatalf("rebind --host %q was accepted; it defeats host binding", host)
			}
		}
		// The refusals wrote nothing.
		record, err := cli.keychain.Get("gh_bot")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if want := []string{"api.github.com"}; len(record.Hosts) != 1 || record.Hosts[0] != want[0] {
			t.Fatalf("hosts = %v, want %v unchanged", record.Hosts, want)
		}
	})

	t.Run("rebind_of_an_env_managed_name_is_refused", func(t *testing.T) {
		t.Setenv("ENV_TOKEN", "ENV-VALUE")
		env := credentials.NewEnv(map[string]credentials.EnvSpec{
			"env_own": {Var: "ENV_TOKEN", Hosts: []string{"api.stripe.com"}},
		})
		cli := newCredentialCLI(t, env)

		_, _, err := cli.run(t, "", "rebind", "env_own", "--host", "api.stripe.com")
		if err == nil {
			t.Fatal("rebind of an env_credentials name returned nil; it must exit 1")
		}
		for _, want := range []string{"config.json", "kill -HUP"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q should name %q", err, want)
			}
		}
	})

	t.Run("rm_of_a_stale_index_entry_says_so", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)
		if err := credentials.NewIndexAt(cli.indexPath).Add("ghost"); err != nil {
			t.Fatalf("seeding a stale entry: %v", err)
		}

		stdout, _, err := cli.run(t, "", "rm", "ghost")
		if err != nil {
			t.Fatalf("rm: %v", err)
		}
		if want := `removed stale index entry "ghost"`; !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}

		// A name held by neither store still exits 0, but must not claim a
		// deletion: an operator revoking a leaked token after a typo would
		// read "removed" and stop there.
		stdout, _, err = cli.run(t, "", "rm", "ghost")
		if err != nil {
			t.Fatalf("second rm: %v", err)
		}
		if want := "nothing was removed"; !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
		if strings.Contains(stdout, `removed "ghost"`) {
			t.Fatalf("stdout = %q claims a deletion that did not happen", stdout)
		}
	})

	t.Run("rm_of_an_env_managed_name_is_refused", func(t *testing.T) {
		t.Setenv("ENV_TOKEN", "ENV-VALUE")
		env := credentials.NewEnv(map[string]credentials.EnvSpec{
			"env_own": {Var: "ENV_TOKEN", Hosts: []string{"api.stripe.com"}},
		})
		cli := newCredentialCLI(t, env)

		stdout, _, err := cli.run(t, "", "rm", "env_own")
		if err == nil {
			t.Fatal("rm of an env_credentials name returned nil; the credential is still being injected")
		}
		if strings.Contains(stdout, "removed") {
			t.Fatalf("stdout = %q claims a removal that did not happen", stdout)
		}
		for _, want := range []string{"config.json", "kill -HUP"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q should name %q", err, want)
			}
		}
	})

	t.Run("set_warns_when_it_shadows_an_env_entry", func(t *testing.T) {
		t.Setenv("SHADOWED_TOKEN", "ENV-VALUE")
		env := credentials.NewEnv(map[string]credentials.EnvSpec{
			"gh_bot": {Var: "SHADOWED_TOKEN", Hosts: []string{"api.github.com"}},
		})
		cli := newCredentialCLI(t, env)

		stdout, stderr, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com")
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if !strings.Contains(stdout, `stored "gh_bot"`) {
			t.Fatalf("stdout = %q, want the success line", stdout)
		}
		if !strings.Contains(stderr, "shadow") {
			t.Fatalf("stderr = %q, want a warning that this shadows the env entry", stderr)
		}
		if strings.Contains(stdout+stderr, cliSecret) {
			t.Fatalf("the value was printed: %q", stdout+stderr)
		}
	})

	t.Run("a_corrupt_index_fails_list_get_and_rebind", func(t *testing.T) {
		cli := newCredentialCLI(t, nil)
		if err := cli.keychain.Set("gh_bot", cliSecret, []string{"api.github.com"}); err != nil {
			t.Fatalf("seeding the keychain: %v", err)
		}
		if err := os.WriteFile(cli.indexPath, []byte("not json"), 0o600); err != nil {
			t.Fatalf("corrupting the index: %v", err)
		}

		for _, tc := range []struct {
			name string
			args []string
		}{
			{"list", []string{"list"}},
			{"get", []string{"get", "gh_bot"}},
			{"rebind", []string{"rebind", "gh_bot", "--host", "api.github.com"}},
		} {
			_, _, err := cli.run(t, "", tc.args...)
			if err == nil {
				t.Fatalf("%s over a corrupt index returned nil; it must exit 1", tc.name)
			}
			if !strings.Contains(err.Error(), cli.indexPath) {
				t.Fatalf("%s error %q does not name the index path", tc.name, err)
			}
			if !strings.Contains(err.Error(), "deleting the file is safe") {
				t.Fatalf("%s error %q does not say deleting the file is safe", tc.name, err)
			}
		}
	})

	t.Run("missing_config_yields_no_env_rows_and_creates_nothing", func(t *testing.T) {
		// The real store, not an injected one: this is about what
		// newCredentialStore does to the filesystem.
		configHome := t.TempDir()
		dataHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)
		t.Setenv("XDG_DATA_HOME", dataHome)

		// An empty index means List never consults the keychain, so this stays
		// safe on a host with no Secret Service.
		stdout, _, err := runCredentialArgs(t, "", "list")
		if err != nil {
			t.Fatalf("list with no config.json returned %v", err)
		}
		if !strings.Contains(stdout, "no credentials indexed") {
			t.Fatalf("stdout = %q, want the empty-index message", stdout)
		}

		created := filepath.Join(configHome, "http-broker", "config.json")
		if _, statErr := os.Stat(created); !os.IsNotExist(statErr) {
			t.Fatalf("a read-only command created %s", created)
		}
	})

	t.Run("a_malformed_config_warns_but_still_lists", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
			t.Fatalf("writing a malformed config: %v", err)
		}

		env, warning := credentialEnv(path)
		if env != nil {
			t.Fatal("a malformed config must yield no env source")
		}
		if !strings.Contains(warning, path) {
			t.Fatalf("warning %q should name the config path", warning)
		}

		// And with no env source, keychain rows still render.
		cli := newCredentialCLI(t, env)
		if _, _, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com"); err != nil {
			t.Fatalf("set: %v", err)
		}
		stdout, _, err := cli.run(t, "", "list")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(stdout, "gh_bot") {
			t.Fatalf("stdout = %q, want the keychain row", stdout)
		}
	})
}

func TestRenderCredentials(t *testing.T) {
	var buf bytes.Buffer
	err := renderCredentials(&buf, []credentials.Metadata{
		{Name: "gh_bot", Source: credentials.SourceKeychain, Hosts: []string{"api.github.com", "uploads.github.com"}, Bytes: 40},
		// Bytes 0 means the size is unknown — an env variable that is not set —
		// which is a different claim from "zero bytes long".
		{Name: "svc_api", Source: credentials.SourceEnv, Hosts: []string{"api.stripe.com"}, Bytes: 0},
	})
	if err != nil {
		t.Fatalf("renderCredentials: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header and two rows: %q", len(lines), buf.String())
	}
	for _, want := range []string{"NAME", "SOURCE", "HOSTS", "BYTES"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("header %q is missing the %s column", lines[0], want)
		}
	}
	if !strings.Contains(lines[1], "api.github.com,uploads.github.com") {
		t.Fatalf("row %q should join hosts with commas", lines[1])
	}
	if !strings.Contains(lines[1], "40") {
		t.Fatalf("row %q should carry the byte count", lines[1])
	}
	if !strings.HasSuffix(lines[2], "-") {
		t.Fatalf("row %q should show %q for an unknown byte count", lines[2], "-")
	}
}

// TestCredentialCommandsNeverPrintAValue is the AC-8 backstop: every command,
// on both its success and failure paths, with a value present in the store.
func TestCredentialCommandsNeverPrintAValue(t *testing.T) {
	cli := newCredentialCLI(t, nil)
	if _, _, err := cli.run(t, cliSecret, "set", "gh_bot", "--host", "api.github.com"); err != nil {
		t.Fatalf("set: %v", err)
	}

	runs := [][]string{
		{"list"},
		{"get", "gh_bot"},
		{"get", "absent"},
		{"rebind", "gh_bot", "--host", "uploads.github.com"},
		{"rebind", "absent", "--host", "uploads.github.com"},
		{"rm", "gh_bot"},
	}
	for _, args := range runs {
		stdout, stderr, err := cli.run(t, "", args...)
		text := stdout + stderr
		if err != nil {
			text += err.Error()
		}
		if strings.Contains(text, cliSecret) {
			t.Fatalf("`credential %s` printed the value: %q", strings.Join(args, " "), text)
		}
	}
}
