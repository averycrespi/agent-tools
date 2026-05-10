package cmd

import (
	"fmt"
	"io"
	"os"

	sbexec "github.com/averycrespi/agent-tools/sandbox-manager/internal/exec"
	"github.com/spf13/cobra"
)

var execWorkdir string

var execCmd = &cobra.Command{
	Use:                "exec [--workdir <path>] -- <command...>",
	Short:              "Run a non-interactive command in the sandbox",
	DisableFlagParsing: false,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) == 0 {
			return fmt.Errorf("command is required")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		return runExecCommand(svc, execWorkdir, args, os.Stdin, os.Stdout, os.Stderr)
	},
}

type execProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
}

type execService interface {
	ExecPiped(workdir string, args ...string) (sbexec.Process, error)
}

func runExecCommand(service execService, workdir string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	proc, err := service.ExecPiped(workdir, args...)
	if err != nil {
		return err
	}
	return runExecProcess(proc, stdin, stdout, stderr)
}

func runExecProcess(proc execProcess, stdin io.Reader, stdout, stderr io.Writer) error {
	stdinDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(proc.Stdin(), stdin)
		if closeErr := proc.Stdin().Close(); err == nil {
			err = closeErr
		}
		stdinDone <- err
	}()

	outputDone := make(chan error, 2)
	go func() {
		_, err := io.Copy(stdout, proc.Stdout())
		outputDone <- err
	}()
	go func() {
		_, err := io.Copy(stderr, proc.Stderr())
		outputDone <- err
	}()

	waitErr := proc.Wait()
	for range 2 {
		if copyErr := <-outputDone; copyErr != nil && waitErr == nil {
			waitErr = copyErr
		}
	}
	select {
	case copyErr := <-stdinDone:
		if copyErr != nil && waitErr == nil {
			waitErr = copyErr
		}
	default:
	}
	return waitErr
}

func init() {
	execCmd.Flags().StringVar(&execWorkdir, "workdir", "/", "working directory inside the sandbox")
	rootCmd.AddCommand(execCmd)
}
