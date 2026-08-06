package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/averycrespi/agent-tools/http-broker/internal/config"
	"github.com/averycrespi/agent-tools/http-broker/internal/credentials"
	"github.com/averycrespi/agent-tools/http-broker/internal/hostmatch"
	"github.com/averycrespi/agent-tools/http-broker/internal/hostnorm"
)

// reloadNote is what every command that changes a binding says about when a
// running proxy picks the change up.
const reloadNote = "a running proxy applies this within 30s (the credential cache TTL); to apply it now:\n" +
	"  kill -HUP $(pgrep -f 'http-broker serve')"

var credentialCmd = &cobra.Command{
	Use:   "credential",
	Short: "Manage credentials injected into intercepted requests",
	Long: "Credentials live in the OS keychain, each stored with the host globs it may\n" +
		"be sent to. Rules reference them as ${cred.<name>}.\n\n" +
		"Values are never printed by any subcommand. `list` and `get` show a name, its\n" +
		"source, its bound hosts, and the byte count of the stored value.\n\n" +
		"The keychain cannot be enumerated, so the names of stored credentials are\n" +
		"recorded in a separate index file holding names only. Deleting that file\n" +
		"loses no secret; `credential get <name>` re-registers a name.",
}

// newCredentialStore builds the credential store commands write through.
//
// It is a variable so tests can inject a fake-backed store; production wires
// the real keychain, the real index, and the env_credentials block from
// config.json.
var newCredentialStore = defaultCredentialStore

func defaultCredentialStore() (*credentials.Store, error) {
	env, warning := credentialEnv(configPath())
	if warning != "" {
		// Written directly rather than through cmd, because a broken
		// config.json must not stop a keychain-only listing from rendering.
		fmt.Fprintln(os.Stderr, warning)
	}
	return credentials.NewStore(env), nil
}

// credentialEnv reads the env_credentials block from config.json, returning a
// nil Env when the file does not exist and a warning string when it cannot be
// parsed.
//
// The os.Stat comes first on purpose: config.Load writes a default config.json
// when the path is absent, and a command run to inspect state must not leave a
// new file behind.
func credentialEnv(path string) (*credentials.Env, string) {
	if _, err := os.Stat(path); err != nil {
		return nil, ""
	}

	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Sprintf("warning: %s could not be read, so env_credentials entries are not listed: %v", path, err)
	}
	if len(cfg.EnvCredentials) == 0 {
		return nil, ""
	}

	specs := make(map[string]credentials.EnvSpec, len(cfg.EnvCredentials))
	for name, ec := range cfg.EnvCredentials {
		specs[name] = credentials.EnvSpec{Var: ec.Var, Hosts: ec.Hosts}
	}
	return credentials.NewEnv(specs), ""
}

// oneCredentialName builds an Args function that names the missing argument
// and shows the command's own example, rather than letting Cobra emit
// "accepts 1 arg(s), received 0".
func oneCredentialName(example string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("missing credential name\n\nexample: %s", example)
		}
		if len(args) > 1 {
			return fmt.Errorf("expected one credential name, got %d: %s", len(args), strings.Join(args, " "))
		}
		return nil
	}
}

var credentialSetHosts []string

var credentialSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Store a credential, reading its value from stdin",
	Long: "Reads the credential value from stdin and stores it in the OS keychain\n" +
		"together with its bound hosts, and records the name in the credential index\n" +
		"so `credential list` can show it.\n\n" +
		"At least one --host is required. The binding is enforced on every request:\n" +
		"a rule that matches a host outside it is refused rather than injected, so a\n" +
		"rule-authoring slip cannot send the token somewhere it does not belong.\n\n" +
		"Example:\n" +
		"  printf %s \"$TOKEN\" | http-broker credential set gh_bot --host api.github.com",
	Args: oneCredentialName(`printf %s "$TOKEN" | http-broker credential set gh_bot --host api.github.com`),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if len(credentialSetHosts) == 0 {
			return errors.New("at least one --host is required; every credential carries host scope")
		}
		if err := validateHostGlobs(credentialSetHosts); err != nil {
			return err
		}

		value, err := readSecretFromStdin(cmd.InOrStdin())
		if err != nil {
			return err
		}

		store, err := newCredentialStore()
		if err != nil {
			return err
		}

		err = store.Set(name, value, credentialSetHosts)
		if err != nil && !errors.Is(err, credentials.ErrIndexNotUpdated) {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "stored %q for %s\n", name, strings.Join(credentialSetHosts, ", "))
		if err != nil {
			// The requested action succeeded and only the listing is missing.
			// Exiting non-zero would invite a re-run of a command whose piped
			// stdin has already been consumed.
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: %v\n  The credential is stored and will be injected, but it is not listed.\n"+
					"  Re-register the name with `http-broker credential get %s`.\n", err, name)
		}
		return nil
	},
}

var credentialRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a credential from the keychain",
	Args:  oneCredentialName("http-broker credential rm gh_bot"),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := newCredentialStore()
		if err != nil {
			return err
		}

		switch err := store.Delete(args[0]); {
		case err == nil:
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed %q\n", args[0])
		case errors.Is(err, credentials.ErrStaleIndexEntry):
			// The name was indexed but the keychain held nothing. Saying
			// "removed" would claim a secret was revoked that was already gone.
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed stale index entry %q\n", args[0])
		default:
			return err
		}
		return nil
	},
}

var credentialListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stored credentials, their sources, and their bound hosts",
	Long: "Lists every indexed keychain credential and every env_credentials entry,\n" +
		"with its bound hosts and the byte count of its value. Values are never\n" +
		"printed.\n\n" +
		"An index entry whose keychain item is gone is pruned and reported.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		store, err := newCredentialStore()
		if err != nil {
			return err
		}

		listing, err := store.List()
		if err != nil {
			return err
		}

		if n := len(listing.Pruned); n > 0 {
			// stderr, so a piped stdout stays a clean table.
			noun := "entries"
			if n == 1 {
				noun = "entry"
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "pruned %d stale %s: %s\n",
				n, noun, strings.Join(listing.Pruned, ", "))
		}

		if len(listing.Credentials) == 0 {
			// An empty index does not mean nothing is stored: the keychain
			// cannot be enumerated, so a wiped index leaves live credentials
			// invisible here until they are named again.
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(out, "no credentials indexed")
			_, _ = fmt.Fprintln(out, "  `http-broker credential set <name> --host <host>` registers a new credential.")
			_, _ = fmt.Fprintln(out, "  `http-broker credential get <name>` re-registers one that is already stored.")
			_, _ = fmt.Fprintln(out, "  The names referenced by rules.json, and the dashboard's Credentials view,")
			_, _ = fmt.Fprintln(out, "  are where to look for what to re-register.")
			return nil
		}

		return renderCredentials(cmd.OutOrStdout(), listing.Credentials)
	},
}

var credentialGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show one credential's source, bound hosts, and value size",
	Long: "Prints the same columns as `list` for a single credential, and re-registers\n" +
		"the name in the credential index if the keychain holds it. That is how a\n" +
		"deleted or corrupt index is repaired: the names are recoverable, the values\n" +
		"never left the keychain.\n\n" +
		"The value is never printed.",
	Args: oneCredentialName("http-broker credential get gh_bot"),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		store, err := newCredentialStore()
		if err != nil {
			return err
		}

		meta, err := store.Describe(name)
		if errors.Is(err, credentials.ErrNotFound) {
			return fmt.Errorf("no credential named %q: the keychain holds no item by that name, and config.json configures no env_credentials entry for it", name)
		}
		if err != nil {
			return err
		}

		// The precedence decision comes from credentials.SourceFor, so the CLI
		// and the dashboard cannot disagree about which source wins.
		if _, shadowed := credentials.SourceFor(meta.Source == credentials.SourceKeychain, store.EnvManaged(name)); shadowed {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: config.json also configures an env_credentials entry named %q, which is shadowed.\n"+
					"  The keychain wins at resolution, so the keychain entry above is the one injected.\n", name)
		}

		return renderCredentials(cmd.OutOrStdout(), []credentials.Metadata{meta})
	},
}

var credentialRebindHosts []string

var credentialRebindCmd = &cobra.Command{
	Use:   "rebind <name>",
	Short: "Change the hosts an existing credential may be sent to",
	Long: "Re-scopes a stored credential to a new host list, keeping its value. The\n" +
		"value is neither read from stdin nor printed.\n\n" +
		"Example:\n" +
		"  http-broker credential rebind gh_bot --host api.github.com --host uploads.github.com",
	Args: oneCredentialName("http-broker credential rebind gh_bot --host api.github.com"),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if len(credentialRebindHosts) == 0 {
			return errors.New("at least one --host is required; every credential carries host scope")
		}
		if err := validateHostGlobs(credentialRebindHosts); err != nil {
			return err
		}

		store, err := newCredentialStore()
		if err != nil {
			return err
		}

		old, err := store.Rebind(name, credentialRebindHosts)
		if errors.Is(err, credentials.ErrEnvManaged) {
			return fmt.Errorf("%q is an env_credentials entry, which config.json owns; edit its hosts there, then:\n  kill -HUP $(pgrep -f 'http-broker serve')", name)
		}
		if err != nil && !errors.Is(err, credentials.ErrIndexNotUpdated) {
			return err
		}

		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rebound %q: %s -> %s\n",
			name, strings.Join(old, ", "), strings.Join(credentialRebindHosts, ", "))
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), reloadNote)
		if err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: %v\n  Re-register the name with `http-broker credential get %s`.\n", err, name)
		}
		return nil
	},
}

// renderCredentials writes the NAME / SOURCE / HOSTS / BYTES table.
//
// There is deliberately no value column, and Metadata has no field that could
// supply one.
func renderCredentials(w io.Writer, rows []credentials.Metadata) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tSOURCE\tHOSTS\tBYTES"); err != nil {
		return err
	}
	for _, row := range rows {
		// A zero byte count means the value could not be sized — an
		// env_credentials variable that is not set, typically — which is not
		// the same claim as "zero bytes long".
		size := "-"
		if row.Bytes > 0 {
			size = fmt.Sprint(row.Bytes)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			row.Name, row.Source, strings.Join(row.Hosts, ","), size); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// readSecretFromStdin reads a credential value from stdin.
//
// It refuses an interactive terminal. A secret typed at a prompt lands in
// shell history and scrollback; requiring a pipe keeps it in neither.
func readSecretFromStdin(in io.Reader) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return "", errors.New("refusing to read a credential from a terminal; pipe it instead:\n" +
			"  printf %s \"$TOKEN\" | http-broker credential set <name> --host <host>")
	}

	data, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("reading the credential from stdin: %w", err)
	}

	// Trim only a trailing newline, which `echo` and most editors add. Other
	// whitespace could legitimately be part of a secret.
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", errors.New("no credential value on stdin")
	}
	return value, nil
}

// validateHostGlobs rejects bindings that would defeat the point of binding.
func validateHostGlobs(hosts []string) error {
	for _, h := range hosts {
		normalized, err := hostnorm.NormalizeGlob(h)
		if err != nil {
			return fmt.Errorf("--host %q: %w", h, err)
		}
		if normalized == "*" || normalized == "**" {
			return fmt.Errorf("--host %q matches every host, which defeats host binding", h)
		}
		if isSuffix, suffix := hostmatch.MatchesPublicSuffix(normalized); isSuffix {
			return fmt.Errorf("--host %q reduces to the public suffix %q, binding the credential to every host under a registry-controlled TLD", h, suffix)
		}
	}
	return nil
}

func init() {
	credentialSetCmd.Flags().StringArrayVar(&credentialSetHosts, "host", nil,
		"host glob this credential may be sent to (repeatable, at least one required)")
	credentialRebindCmd.Flags().StringArrayVar(&credentialRebindHosts, "host", nil,
		"host glob this credential may be sent to (repeatable, at least one required)")
	credentialCmd.AddCommand(credentialSetCmd, credentialRmCmd, credentialListCmd, credentialGetCmd, credentialRebindCmd)
	rootCmd.AddCommand(credentialCmd)
}
