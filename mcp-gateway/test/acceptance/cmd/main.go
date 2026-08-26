package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/mcp-gateway/test/acceptance"
)

func main() {
	profileName := flag.String("profile", string(acceptance.ProfileS21), "closed acceptance profile")
	flag.Parse()
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve repository root")
		os.Exit(1)
	}
	report := acceptance.RunProfile(context.Background(), root, acceptance.OSExecutor{}, acceptance.Profile(*profileName), false)
	contents, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode acceptance report")
		os.Exit(1)
	}
	if _, err := acceptance.Parse(contents); err != nil {
		fmt.Fprintln(os.Stderr, "validate acceptance report")
		os.Exit(1)
	}
	fmt.Println(string(contents))
	if report.Result != acceptance.ResultPassed {
		os.Exit(1)
	}
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
