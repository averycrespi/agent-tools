package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/averycrespi/agent-tools/egress-broker/internal/config"
	"github.com/averycrespi/agent-tools/egress-broker/internal/credentials"
	"github.com/averycrespi/agent-tools/egress-broker/internal/hostmatch"
	"github.com/averycrespi/agent-tools/egress-broker/internal/hostnorm"
)

var credentialCmd = &cobra.Command{
	Use:   "credential",
	Short: "Manage credentials injected into intercepted requests",
	Long: "Credentials live in the OS keychain, each stored with the host globs it may\n" +
		"be sent to. Rules reference them as ${cred.<name>}.\n\n" +
		"Values are never printed by any subcommand.",
}

var credentialSetHosts []string

var credentialSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Store a credential, reading its value from stdin",
	Long: "Reads the credential value from stdin and stores it in the OS keychain\n" +
		"together with its bound hosts.\n\n" +
		"At least one --host is required. The binding is enforced on every request:\n" +
		"a rule that matches a host outside it is refused rather than injected, so a\n" +
		"rule-authoring slip cannot send the token somewhere it does not belong.\n\n" +
		"Example:\n" +
		"  printf %s \"$TOKEN\" | egress-broker credential set gh_bot --host api.github.com",
	Args: func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return errors.New("missing credential name\n\nexample: printf %s \"$TOKEN\" | egress-broker credential set gh_bot --host api.github.com")
		}
		if len(args) > 1 {
			return fmt.Errorf("expected one credential name, got %d: %s", len(args), strings.Join(args, " "))
		}
		return nil
	},
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

		if err := credentials.NewKeychain().Set(name, value, credentialSetHosts); err != nil {
			return err
		}
		cmd.Printf("stored %q for %s\n", name, strings.Join(credentialSetHosts, ", "))
		return nil
	},
}

var credentialListJSON bool

var credentialListCmd = &cobra.Command{
	Use:   "list",
	Short: "List credential names, sources, and bound hosts",
	Long: "Lists every credential referenced by the rules file, plus every configured\n" +
		"env_credentials entry, with the hosts each is bound to.\n\n" +
		"Values are never printed.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(configPath())
		if err != nil {
			return err
		}

		metas, err := collectCredentialMetadata(cfg)
		if err != nil {
			return err
		}

		if credentialListJSON {
			out, err := json.MarshalIndent(metas, "", "  ")
			if err != nil {
				return fmt.Errorf("rendering JSON: %w", err)
			}
			cmd.Println(string(out))
			return nil
		}

		if len(metas) == 0 {
			cmd.Println("no credentials configured")
			return nil
		}
		for _, m := range metas {
			cmd.Printf("%-24s %-16s %s\n", m.Name, m.Source, strings.Join(m.Hosts, ", "))
		}
		return nil
	},
}

var credentialRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a credential from the keychain",
	Args: func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return errors.New("missing credential name\n\nexample: egress-broker credential rm gh_bot")
		}
		if len(args) > 1 {
			return fmt.Errorf("expected one credential name, got %d: %s", len(args), strings.Join(args, " "))
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := credentials.NewKeychain().Delete(args[0]); err != nil {
			return err
		}
		cmd.Printf("removed %q\n", args[0])
		return nil
	},
}

// readSecretFromStdin reads a credential value from stdin.
//
// It refuses an interactive terminal. A secret typed at a prompt lands in
// shell history and scrollback; requiring a pipe keeps it in neither.
func readSecretFromStdin(in io.Reader) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return "", errors.New("refusing to read a credential from a terminal; pipe it instead:\n" +
			"  printf %s \"$TOKEN\" | egress-broker credential set <name> --host <host>")
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

// collectCredentialMetadata gathers metadata for every credential the running
// configuration knows about, without reading any value into the output.
func collectCredentialMetadata(cfg config.Config) ([]credentials.Metadata, error) {
	var metas []credentials.Metadata

	envSpecs := make(map[string]credentials.EnvSpec, len(cfg.EnvCredentials))
	for name, ec := range cfg.EnvCredentials {
		envSpecs[name] = credentials.EnvSpec{Var: ec.Var, Hosts: ec.Hosts}
	}
	env := credentials.NewEnv(envSpecs)
	for _, name := range env.Names() {
		meta, err := env.Describe(name)
		if err != nil {
			return nil, fmt.Errorf("describing env credential %q: %w", name, err)
		}
		metas = append(metas, meta)
	}

	// Keychain items are only discoverable by name, so the rules file is what
	// tells us which names to ask about.
	keychain := credentials.NewKeychain()
	for _, name := range referencedCredentialNames(cfg) {
		if _, isEnv := envSpecs[name]; isEnv {
			continue
		}
		meta, err := keychain.Describe(name)
		if err != nil {
			// A referenced-but-absent credential is worth showing rather than
			// hiding: it is exactly the misconfiguration that produces a 403.
			metas = append(metas, credentials.Metadata{Name: name, Source: "keychain (unavailable)"})
			continue
		}
		metas = append(metas, meta)
	}

	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	return metas, nil
}

// referencedCredentialNames returns the credential names the rules file
// references, sorted.
func referencedCredentialNames(cfg config.Config) []string {
	path := config.EffectiveRulesPath(configPath(), cfg)
	doc, err := config.LoadRulesFile(path)
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	for _, rule := range doc.Rules {
		if rule.Inject == nil {
			continue
		}
		for _, name := range credentials.ReferencesIn(rule.Inject.Set) {
			seen[name] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func init() {
	credentialSetCmd.Flags().StringArrayVar(&credentialSetHosts, "host", nil,
		"host glob this credential may be sent to (repeatable, at least one required)")
	credentialListCmd.Flags().BoolVar(&credentialListJSON, "json", false, "output as JSON")

	credentialCmd.AddCommand(credentialSetCmd, credentialListCmd, credentialRmCmd)
	rootCmd.AddCommand(credentialCmd)
}
