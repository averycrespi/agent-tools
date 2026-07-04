package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/grants"
)

var grantCmd = &cobra.Command{
	Use:   "grant",
	Short: "Manage temporary policy grants",
}

var grantMintCmd = &cobra.Command{
	Use:   "mint",
	Short: "Mint a temporary grant token",
	Args:  cobra.NoArgs,
	RunE:  runGrantMint,
}

var grantListCmd = &cobra.Command{
	Use:   "list",
	Short: "List retained grants",
	Args:  cobra.NoArgs,
	RunE:  runGrantList,
}

var grantRevokeCmd = &cobra.Command{
	Use:   "revoke <grant-id-or-fingerprint>",
	Short: "Revoke a grant",
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("missing grant ID or fingerprint\n\nUsage: %s", cmd.UseLine())
		}
		if len(args) > 1 {
			return fmt.Errorf("expected one grant ID or fingerprint, got %d\n\nUsage: %s", len(args), cmd.UseLine())
		}
		return nil
	},
	RunE: runGrantRevoke,
}

func init() {
	grantMintCmd.Flags().String("name", "", "grant name (required)")
	grantMintCmd.Flags().String("ttl", "", "grant TTL as a Go duration, for example 1h or 30m (required)")
	grantMintCmd.Flags().String("rules-file", "", "grant rules JSON file path, or - for stdin (required)")
	grantMintCmd.Flags().String("description", "", "grant description")
	grantMintCmd.Flags().Bool("json", false, "print minted grant as JSON")

	grantListCmd.Flags().Bool("json", false, "print grants as JSON")
	grantRevokeCmd.Flags().Bool("json", false, "print revoked grant as JSON")

	grantCmd.AddCommand(grantMintCmd)
	grantCmd.AddCommand(grantListCmd)
	grantCmd.AddCommand(grantRevokeCmd)
	rootCmd.AddCommand(grantCmd)
}

func runGrantMint(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	ttlRaw, _ := cmd.Flags().GetString("ttl")
	rulesPath, _ := cmd.Flags().GetString("rules-file")
	description, _ := cmd.Flags().GetString("description")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("--name is required")
	}
	if strings.TrimSpace(ttlRaw) == "" {
		return fmt.Errorf("--ttl is required")
	}
	if strings.TrimSpace(rulesPath) == "" {
		return fmt.Errorf("--rules-file is required")
	}
	ttl, err := time.ParseDuration(ttlRaw)
	if err != nil {
		return fmt.Errorf("parse --ttl: %w", err)
	}

	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	ruleData, err := readGrantRules(cmd, rulesPath)
	if err != nil {
		return err
	}
	ruleConfigs, err := grants.ParseRulesFile(ruleData)
	if err != nil {
		return err
	}

	store, err := grants.Open(cfg.Grants.Path)
	if err != nil {
		return fmt.Errorf("opening grants db: %w", err)
	}
	defer func() { _ = store.Close() }()

	minted, err := store.Mint(commandContext(cmd), grants.MintOptions{
		Name:        name,
		Description: description,
		TTL:         ttl,
		MaxTTL:      time.Duration(cfg.Grants.MaxTTLSeconds) * time.Second,
		Rules:       ruleConfigs,
	})
	if err != nil {
		return err
	}

	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(minted)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Grant minted\n")
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", minted.Grant.ID)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\n", minted.Grant.Name)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Fingerprint: %s\n", minted.Grant.Fingerprint)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Expires: %s\n", minted.Grant.ExpiresAt.Format(time.RFC3339))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Token: %s\n", minted.Token)
	return nil
}

func runGrantList(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	store, err := grants.Open(cfg.Grants.Path)
	if err != nil {
		return fmt.Errorf("opening grants db: %w", err)
	}
	defer func() { _ = store.Close() }()

	items, err := store.List(commandContext(cmd), time.Now())
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ID\tFINGERPRINT\tSTATUS\tEXPIRES\tNAME\n")
	for _, g := range items {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", g.ID, g.Fingerprint, g.Status, g.ExpiresAt.Format(time.RFC3339), g.Name)
	}
	return nil
}

func runGrantRevoke(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	store, err := grants.Open(cfg.Grants.Path)
	if err != nil {
		return fmt.Errorf("opening grants db: %w", err)
	}
	defer func() { _ = store.Close() }()

	revoked, err := store.Revoke(commandContext(cmd), args[0], time.Now())
	if errors.Is(err, grants.ErrUnknown) {
		return fmt.Errorf("grant not found: %s", args[0])
	}
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(revoked)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Revoked grant %s (%s)\n", revoked.ID, revoked.Fingerprint)
	return nil
}

func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func readGrantRules(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, fmt.Errorf("read grant rules from stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read grant rules file: %w", err)
	}
	return data, nil
}

func grantStoreFromConfig(ctx context.Context) (*grants.Store, config.Config, error) {
	_ = ctx
	cfg, err := config.Load(configPath())
	if err != nil {
		return nil, cfg, fmt.Errorf("loading config: %w", err)
	}
	store, err := grants.Open(cfg.Grants.Path)
	if err != nil {
		return nil, cfg, fmt.Errorf("opening grants db: %w", err)
	}
	return store, cfg, nil
}
