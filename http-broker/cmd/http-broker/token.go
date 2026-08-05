package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/http-broker/internal/auth"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage the shared bearer token",
	Long: "One token authenticates both listeners: the proxy accepts it as a\n" +
		"Proxy-Authorization credential, the dashboard as a Bearer header or cookie.",
}

var tokenShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the current token, generating one if absent",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		token, err := auth.EnsureToken(auth.TokenPath())
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), token)
		return nil
	},
}

var tokenProxyURLCmd = &cobra.Command{
	Use:   "proxy-credential",
	Short: "Print the Proxy-Authorization header value clients should send",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		token, err := auth.EnsureToken(auth.TokenPath())
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), auth.ProxyCredential(token))
		return nil
	},
}

var tokenRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Generate a new token, invalidating the old one",
	Long: "Replaces the token. Every sandbox and dashboard session using the old one\n" +
		"stops working immediately: re-run provisioning in each sandbox, then send\n" +
		"SIGHUP to a running serve.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		token, err := auth.Generate()
		if err != nil {
			return err
		}
		if err := auth.Write(auth.TokenPath(), token); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), token)
		cmd.PrintErrln("re-run provisioning in every sandbox, then send SIGHUP to a running serve")
		return nil
	},
}

func init() {
	tokenCmd.AddCommand(tokenShowCmd, tokenRotateCmd, tokenProxyURLCmd)
	rootCmd.AddCommand(tokenCmd)
}
