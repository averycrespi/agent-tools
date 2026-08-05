package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/averycrespi/agent-tools/http-broker/internal/credentials"
	"github.com/averycrespi/agent-tools/http-broker/internal/hostmatch"
	"github.com/averycrespi/agent-tools/http-broker/internal/hostnorm"
)

var credentialCmd = &cobra.Command{
	Use:   "credential",
	Short: "Manage credentials injected into intercepted requests",
	Long: "Credentials live in the OS keychain, each stored with the host globs it may\n" +
		"be sent to. Rules reference them as ${cred.<name>}.\n\n" +
		"Values are never printed by any subcommand. To see which credentials the\n" +
		"running policy references and what they are bound to, use the dashboard's\n" +
		"Credentials view.",
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
		"  printf %s \"$TOKEN\" | http-broker credential set gh_bot --host api.github.com",
	Args: func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return errors.New("missing credential name\n\nexample: printf %s \"$TOKEN\" | http-broker credential set gh_bot --host api.github.com")
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

var credentialRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a credential from the keychain",
	Args: func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return errors.New("missing credential name\n\nexample: http-broker credential rm gh_bot")
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
	credentialCmd.AddCommand(credentialSetCmd, credentialRmCmd)
	rootCmd.AddCommand(credentialCmd)
}
