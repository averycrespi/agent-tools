package acceptance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
)

const (
	ResultPassed = "passed"
	ResultFailed = "failed"
)

type Command struct {
	CheckName string
	Name      string
	Arguments []string
	Artifacts []string
	Native    bool
	Timeout   time.Duration
}

type Executor interface {
	Run(context.Context, string, Command) ([]byte, error)
}

type OSExecutor struct{}

type commandExecutionError struct {
	cause           error
	termination     string
	cleanup         string
	diagnosticPaths []string
}

func (failure *commandExecutionError) Error() string { return failure.cause.Error() }
func (failure *commandExecutionError) Unwrap() error { return failure.cause }

func (OSExecutor) Run(ctx context.Context, root string, command Command) ([]byte, error) {
	return runOSCommand(ctx, root, command, true, os.Stderr)
}

func runOSCommand(ctx context.Context, root string, command Command, cleanupInherited bool, stdout io.Writer) ([]byte, error) {
	runner, err := testutil.NewBinaryRunner(19*time.Minute, 4*1024*1024)
	if err != nil {
		return nil, err
	}
	result, runErr := runner.RunInDir(ctx, root, command.Name, command.Arguments...)
	_, stdoutErr := stdout.Write(result.Stdout)
	_, _ = os.Stderr.Write(result.Stderr)
	var cleanupErr error
	if cleanupInherited {
		cleanupErr = testutil.CleanupInheritedProcesses()
	}
	if result.StdoutTruncated || result.StderrTruncated {
		cleanupErr = errors.Join(cleanupErr, errors.New("acceptance command output exceeded its bound"))
	}
	combinedErr := errors.Join(runErr, cleanupErr, stdoutErr)
	if combinedErr != nil {
		termination := "none"
		if result.Cleanup.KillSent {
			termination = "kill"
		} else if result.Cleanup.TermSent {
			termination = "term"
		}
		cleanup := "passed"
		if cleanupErr != nil || result.Cleanup.Survived {
			cleanup = "failed"
		}
		combinedErr = &commandExecutionError{cause: combinedErr, termination: termination, cleanup: cleanup}
	}
	if command.Native {
		return result.Stdout, combinedErr
	}
	return nil, combinedErr
}

func validateTiming(startedAt, endedAt string, durationMillis int64) error {
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return errors.New("invalid start timestamp")
	}
	ended, err := time.Parse(time.RFC3339Nano, endedAt)
	if err != nil || ended.Before(started) {
		return errors.New("invalid end timestamp")
	}
	if ended.Sub(started).Milliseconds() != durationMillis {
		return errors.New("duration does not match timestamps")
	}
	return nil
}

func executionMetadata(err error) (string, string, []string) {
	if err == nil {
		return "none", "passed", []string{}
	}
	termination := "none"
	cleanup := "passed"
	var failure *commandExecutionError
	if errors.As(err, &failure) {
		termination = failure.termination
		cleanup = failure.cleanup
		return termination, cleanup, append([]string(nil), failure.diagnosticPaths...)
	}
	return termination, cleanup, []string{}
}

func gitOutput(ctx context.Context, root string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...) //nolint:gosec // Internal callers supply closed Git verbs and a repository root.
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}
