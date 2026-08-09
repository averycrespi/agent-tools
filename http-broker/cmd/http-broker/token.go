package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/http-broker/internal/auth"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage role credentials",
	Long:  "The agent credential authenticates the proxy; the admin credential authenticates the dashboard.",
}

var tokenShowCmd = &cobra.Command{
	Use:   "show <agent|admin>",
	Short: "Print one selected role credential",
	Args:  tokenRoleArgs("show"),
	RunE: func(cmd *cobra.Command, args []string) error {
		role, _ := parseTokenRole(args[0])
		tokens, err := auth.EnsureTokenSetContext(commandContext(cmd), auth.DefaultTokenPaths())
		if err != nil {
			return fmt.Errorf("loading role credentials: %w", err)
		}
		selected := tokens.Agent
		if role == auth.AdminRole {
			selected = tokens.Admin
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), selected)
		return err
	},
}

var tokenProxyURLCmd = &cobra.Command{
	Use:   "proxy-credential",
	Short: "Print the agent-derived Proxy-Authorization header value",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		tokens, err := auth.EnsureTokenSetContext(commandContext(cmd), auth.DefaultTokenPaths())
		if err != nil {
			return fmt.Errorf("loading role credentials: %w", err)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), auth.ProxyCredential(tokens.Agent))
		return err
	},
}

var tokenRotateCmd = &cobra.Command{
	Use:   "rotate <agent|admin>",
	Short: "Rotate one selected role credential",
	Long:  "Rotate one role and activate it with SIGHUP. New CONNECT, absolute-form proxy, and dashboard requests reject the old role value; established tunnel/MITM traffic and dashboard SSE may continue. Agent rotation is a coordinated re-provisioning cutover, not zero-downtime revocation.",
	Args:  tokenRoleArgs("rotate"),
	RunE: func(cmd *cobra.Command, args []string) error {
		role, _ := parseTokenRole(args[0])
		if _, err := auth.RotateTokenContext(commandContext(cmd), auth.DefaultTokenPaths(), role); err != nil {
			return fmt.Errorf("rotating %s token: %w", role, err)
		}
		paths := auth.DefaultTokenPaths()
		var outputErr error
		switch role {
		case auth.AgentRole:
			_, outputErr = fmt.Fprintf(cmd.OutOrStdout(), "Rotated agent token in %s; re-provision agent-token, send SIGHUP promptly, then reconnect old-token clients.\n", paths.Agent)
		case auth.AdminRole:
			_, outputErr = fmt.Fprintf(cmd.OutOrStdout(), "Rotated admin token in %s. Send SIGHUP, then reopen the dashboard to authenticate again.\n", paths.Admin)
		}
		return outputErr
	},
}

func tokenRoleArgs(command string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("token %s requires exactly one role; usage: http-broker token %s <agent|admin>", command, command)
		}
		_, err := parseTokenRole(args[0])
		return err
	}
}

func parseTokenRole(value string) (auth.Role, error) {
	switch auth.Role(value) {
	case auth.AgentRole:
		return auth.AgentRole, nil
	case auth.AdminRole:
		return auth.AdminRole, nil
	default:
		return "", fmt.Errorf("invalid token role %q: expected agent or admin", value)
	}
}

func init() {
	tokenCmd.AddCommand(tokenShowCmd, tokenRotateCmd, tokenProxyURLCmd)
	rootCmd.AddCommand(tokenCmd)
}
