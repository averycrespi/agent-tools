package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/averycrespi/agent-tools/mcp-gateway/test/acceptance"
)

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	profileName := flag.String("profile", string(acceptance.ProfileS21), "closed acceptance profile")
	outputPath := flag.String("output", "", "atomic report or adoption output path")
	adoptPath := flag.String("adopt", "", "immutable acceptance report to adopt without running checks")
	flag.Parse()
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve repository root")
		return 1
	}
	if *adoptPath != "" {
		if *outputPath == "" {
			fmt.Fprintln(os.Stderr, "adoption requires an output path")
			return 1
		}
		if _, err := acceptance.AdoptReport(root, *adoptPath, *outputPath, time.Now); err != nil {
			fmt.Fprintln(os.Stderr, "adopt acceptance report")
			return 1
		}
		return 0
	}
	ledgerRoot, err := os.MkdirTemp("", "mcp-gateway-acceptance-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create acceptance cleanup root")
		return 1
	}
	defer func() {
		if err := os.RemoveAll(ledgerRoot); err != nil {
			fmt.Fprintln(os.Stderr, "remove acceptance cleanup root")
			exitCode = 1
		}
	}()
	ledger, err := testutil.NewCleanupLedger(ledgerRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "initialize acceptance cleanup ledger")
		return 1
	}
	if err := os.Setenv(testutil.CleanupLedgerEnvironment, ledger.Path()); err != nil {
		fmt.Fprintln(os.Stderr, "publish acceptance cleanup ledger")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if readyPath := os.Getenv("MCP_GATEWAY_ACCEPTANCE_SIGNAL_READY"); readyPath != "" {
		if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil { //nolint:gosec // Test-only signal fixture path is explicitly supplied by the caller.
			fmt.Fprintln(os.Stderr, "publish acceptance signal readiness")
			return 1
		}
	}
	if os.Getenv("MCP_GATEWAY_ACCEPTANCE_WAIT_FOR_SIGNAL") == "1" {
		<-ctx.Done()
	}
	report := acceptance.RunProfile(ctx, root, acceptance.OSExecutor{}, acceptance.Profile(*profileName), false)
	if err := ledger.Cleanup(); err != nil {
		report.Result = acceptance.ResultFailed
		report.Reason = "process_cleanup_failed"
	}
	contents, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode acceptance report")
		return 1
	}
	if _, err := acceptance.Parse(contents); err != nil {
		fmt.Fprintln(os.Stderr, "validate acceptance report")
		return 1
	}
	if *outputPath != "" {
		if err := acceptance.WriteReport(*outputPath, report); err != nil {
			fmt.Fprintln(os.Stderr, "write acceptance report")
			return 1
		}
	} else {
		fmt.Println(string(contents))
	}
	if report.Result != acceptance.ResultPassed {
		return 1
	}
	return 0
}

func repositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(current, ".git")); statErr == nil {
			return current, nil
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", os.ErrNotExist
		}
		current = parent
	}
}
