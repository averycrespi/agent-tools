package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// withUsage wraps a cobra.PositionalArgs validator so its error message
// includes the command's usage line. The root command sets SilenceUsage:
// true to keep runtime errors terse, which means Cobra's default arg-
// validation errors ("accepts 1 arg(s), received 0") print with no hint
// about what the argument should be. Wrapping the validator restores that
// hint without re-enabling usage output for runtime errors from RunE.
func withUsage(validator cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validator(cmd, args); err != nil {
			return fmt.Errorf("%w\nUsage: %s", err, cmd.UseLine())
		}
		return nil
	}
}

// exactArgs is cobra.ExactArgs wrapped by withUsage.
func exactArgs(n int) cobra.PositionalArgs { return withUsage(cobra.ExactArgs(n)) }
