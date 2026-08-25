package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/test/acceptance"
)

func main() {
	rootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve repository root")
		os.Exit(1)
	}
	report := acceptance.Run(context.Background(), strings.TrimSpace(string(rootBytes)), acceptance.OSExecutor{}, false)
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
