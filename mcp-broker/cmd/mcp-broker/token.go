package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/auth"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage role credentials",
}

var tokenShowCmd = &cobra.Command{
	Use:   "show <agent|admin>",
	Short: "Print one role credential",
	Args:  tokenRoleArgs("show"),
	RunE: func(cmd *cobra.Command, args []string) error {
		role, _ := parseTokenRole(args[0])
		tokens, err := auth.EnsureTokenSet(auth.DefaultTokenPaths())
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

var tokenRotateCmd = &cobra.Command{
	Use:   "rotate <agent|admin>",
	Short: "Rotate one role credential",
	Args:  tokenRoleArgs("rotate"),
	RunE: func(cmd *cobra.Command, args []string) error {
		role, _ := parseTokenRole(args[0])
		if _, err := auth.RotateToken(auth.DefaultTokenPaths(), role); err != nil {
			return fmt.Errorf("rotating %s token: %w", role, err)
		}
		paths := auth.DefaultTokenPaths()
		switch role {
		case auth.AgentRole:
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Rotated agent token in %s; re-provision agent-token, send SIGHUP promptly, then reconnect old-token clients.\n", paths.Agent)
		case auth.AdminRole:
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Rotated admin token in %s. Send SIGHUP, then reopen the dashboard to authenticate again.\n", paths.Admin)
		}
		return nil
	},
}

func tokenRoleArgs(command string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("token %s requires exactly one role; usage: mcp-broker token %s <agent|admin>", command, command)
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
	tokenCmd.AddCommand(tokenShowCmd, tokenRotateCmd)
	rootCmd.AddCommand(tokenCmd)
}
