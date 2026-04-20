package goenv

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/exec"
)

// Env holds the subset of go env the proxy cares about.
type Env struct {
	GOPRIVATE  string `json:"GOPRIVATE"`
	GOMODCACHE string `json:"GOMODCACHE"`
	GOVERSION  string `json:"GOVERSION"`
}

// Read shells out to `go env -json` and parses the result.
func Read(ctx context.Context, runner exec.Runner) (Env, error) {
	out, err := runner.Run(ctx, "go", "env", "-json", "GOPRIVATE", "GOMODCACHE", "GOVERSION")
	if err != nil {
		return Env{}, fmt.Errorf("running go env: %w: %s", err, out)
	}
	var env Env
	if err := json.Unmarshal(out, &env); err != nil {
		return Env{}, fmt.Errorf("parsing go env output: %w", err)
	}
	return env, nil
}
