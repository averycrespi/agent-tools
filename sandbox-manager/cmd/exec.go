package cmd

import (
	"fmt"
	"io"
	"os"

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
		proc, err := svc.ExecPiped(execWorkdir, args...)
		if err != nil {
			return err
		}

		done := make(chan error, 3)
		go func() {
			_, err := io.Copy(proc.Stdin(), os.Stdin)
			if closeErr := proc.Stdin().Close(); err == nil {
				err = closeErr
			}
			done <- err
		}()
		go func() {
			_, err := io.Copy(os.Stdout, proc.Stdout())
			done <- err
		}()
		go func() {
			_, err := io.Copy(os.Stderr, proc.Stderr())
			done <- err
		}()

		waitErr := proc.Wait()
		for range 3 {
			if copyErr := <-done; copyErr != nil && waitErr == nil {
				waitErr = copyErr
			}
		}
		return waitErr
	},
}

func runExecProcess(proc interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
}, stdin io.Reader, stdout, stderr io.Writer) error {
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
