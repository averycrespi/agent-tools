package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func confirmOffline(command *cobra.Command, confirm bool, consequence string) error {
	if !confirm {
		file, ok := command.InOrStdin().(*os.File)
		if !ok || !term.IsTerminal(file.Fd()) {
			return controlclient.ErrConfirmationRequired
		}
	}
	return controlclient.RequireConfirmation(controlclient.ConfirmationOptions{Yes: confirm, Consequence: consequence})
}

func offlineResult(command *cobra.Command, jsonOutput bool, value any, human string) error {
	mode := controlclient.OutputHuman
	if jsonOutput {
		mode = controlclient.OutputJSON
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	renderer, err := controlclient.NewRenderer(mode, command.OutOrStdout(), command.ErrOrStderr())
	if err != nil {
		return err
	}
	return renderer.WriteFiniteSuccess(data, human)
}

func offlinePlan(command *cobra.Command, jsonOutput bool, value any, human string) error {
	if jsonOutput {
		return json.NewEncoder(command.ErrOrStderr()).Encode(value)
	}
	_, err := fmt.Fprintln(command.ErrOrStderr(), human)
	return err
}

// Flag errors name only parser-owned flag metadata, never the supplied value.
func offlineFlagMessage(err error) string {
	var invalid *pflag.InvalidValueError
	if errors.As(err, &invalid) {
		return "The --" + invalid.GetFlag().Name + " value is invalid."
	}
	var missing *pflag.ValueRequiredError
	if errors.As(err, &missing) {
		return "The --" + missing.GetFlag().Name + " value is required."
	}
	return "A flag is not recognized; see --help."
}

func offlineMode(jsonOutput bool) controlclient.OutputMode {
	if jsonOutput {
		return controlclient.OutputJSON
	}
	return controlclient.OutputHuman
}
