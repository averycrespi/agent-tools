package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/averycrespi/agent-tools/mcp-gateway/test/acceptance"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(arguments []string) int {
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "usage: acceptance <accept|adopt-acceptance-report|qualify-external-evidence|suite-inventory|suite-plan|run-suite>")
		return 2
	}
	for _, argument := range arguments {
		if argument == "--qualify-external" || strings.HasPrefix(argument, "--profile") || strings.HasPrefix(argument, "--task") || strings.HasPrefix(argument, "--milestone") {
			fmt.Fprintln(os.Stderr, "legacy acceptance mode is unsupported")
			return 2
		}
	}
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve repository root")
		return 1
	}
	switch arguments[0] {
	case "suite-inventory":
		if len(arguments) != 1 {
			return 2
		}
		inventory, err := acceptance.DiscoverSuiteInventory(filepath.Join(root, "mcp-gateway"), runtime.GOOS, runtime.GOARCH)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(inventory); err != nil {
			return 1
		}
		return 0
	case "run-suite", "suite-plan":
		return runSuite(root, arguments[0], arguments[1:])
	case "accept":
		return runAccept(root, arguments[1:])
	case "adopt-acceptance-report":
		return runAdopt(root, arguments[1:])
	case "qualify-external-evidence":
		if len(arguments) != 1 {
			fmt.Fprintln(os.Stderr, "qualify-external-evidence accepts no arguments")
			return 2
		}
		if err := acceptance.PrepareAndQualifyFinalExternalEvidence(context.Background(), root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "unknown acceptance command")
		return 2
	}
}

func runSuite(root, operation string, arguments []string) int {
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, operation+" requires a suite owner")
		return 2
	}
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	count := flags.Int("count", 1, "repeat count (stress only)")
	jsonOutput := false
	if operation == "run-suite" {
		flags.BoolVar(&jsonOutput, "json", false, "emit Go test JSON events")
	}
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	if operation == "suite-plan" {
		moduleRoot := filepath.Join(root, "mcp-gateway")
		inventory, err := acceptance.DiscoverSuiteInventory(moduleRoot, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		plan, err := acceptance.PlanSuite(moduleRoot, arguments[0], inventory, *count)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(plan); err != nil {
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := acceptance.RunSuite(ctx, root, arguments[0], *count, acceptance.SuiteExecutor{JSON: jsonOutput}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func runAccept(root string, arguments []string) int {
	flags := flag.NewFlagSet("accept", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	reportPath := flags.String("report", "", "absolute release report path")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*reportPath) {
		fmt.Fprintln(os.Stderr, "accept requires --report <absolute-path>")
		return 2
	}
	initialTemporaryRoots, err := acceptanceTemporaryRoots()
	if err != nil {
		fmt.Fprintln(os.Stderr, "inventory acceptance temporary roots")
		return 1
	}
	ledgerRoot, err := os.MkdirTemp("", "mcp-gateway-acceptance-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create acceptance cleanup root")
		return 1
	}
	ledger, err := testutil.NewCleanupLedger(ledgerRoot)
	if err != nil {
		_ = os.RemoveAll(ledgerRoot)
		fmt.Fprintln(os.Stderr, "initialize acceptance cleanup ledger")
		return 1
	}
	if err := os.Setenv(testutil.CleanupLedgerEnvironment, ledger.Path()); err != nil {
		_ = os.RemoveAll(ledgerRoot)
		fmt.Fprintln(os.Stderr, "publish acceptance cleanup ledger")
		return 1
	}
	finalized := false
	finalize := func() acceptance.ReleaseCleanupResult {
		if finalized {
			return acceptance.ReleaseCleanupResult{Err: fmt.Errorf("release cleanup finalized twice")}
		}
		finalized = true
		cleanupErr := ledger.Cleanup()
		unsetErr := os.Unsetenv(testutil.CleanupLedgerEnvironment)
		removeErr := os.RemoveAll(ledgerRoot)
		remainingTemporaryRoots, temporaryRootsErr := acceptanceTemporaryRoots()
		if temporaryRootsErr == nil && !containsNoNewTemporaryRoots(initialTemporaryRoots, remainingTemporaryRoots) {
			temporaryRootsErr = fmt.Errorf("acceptance left temporary roots")
		}
		return acceptance.ReleaseCleanupResult{
			Processes: cleanupErr == nil, Listeners: cleanupErr == nil,
			TemporaryRoots: removeErr == nil && temporaryRootsErr == nil,
			Err:            errors.Join(cleanupErr, unsetErr, removeErr, temporaryRootsErr),
		}
	}
	defer func() {
		if !finalized {
			_ = finalize()
		}
	}()
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
	passed, err := acceptance.RunFinalAcceptance(ctx, root, *reportPath, acceptance.OSExecutor{}, finalize)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !passed {
		return 1
	}
	return 0
}

func acceptanceTemporaryRoots() (map[string]struct{}, error) {
	roots := make(map[string]struct{})
	for _, pattern := range []string{"mcp-gateway-e2e-*", "mcp-gateway-ui-development-*"} {
		matches, err := filepath.Glob(filepath.Join(os.TempDir(), pattern))
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			roots[match] = struct{}{}
		}
	}
	return roots, nil
}

func containsNoNewTemporaryRoots(before, after map[string]struct{}) bool {
	for root := range after {
		if _, existed := before[root]; !existed {
			return false
		}
	}
	return true
}

func runAdopt(root string, arguments []string) int {
	flags := flag.NewFlagSet("adopt-acceptance-report", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	reportPath := flags.String("report", "", "absolute release report path")
	outputPath := flags.String("output", "", "absolute adoption output path")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*reportPath) || !filepath.IsAbs(*outputPath) {
		fmt.Fprintln(os.Stderr, "adopt-acceptance-report requires --report and --output absolute paths")
		return 2
	}
	if err := acceptance.AdoptFinalAcceptanceReport(root, *reportPath, *outputPath, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, err)
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
