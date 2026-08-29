package main

import "github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"

type executionOptionInput struct {
	DataDir   string
	Output    string
	OutputSet bool
	JSON      bool
}

type executionOptions struct {
	DataDir string
	Output  controlclient.OutputMode
}

func resolveExecutionOptions(input executionOptionInput) (executionOptions, error) {
	rawOutput := input.Output
	if rawOutput == "" {
		rawOutput = string(controlclient.OutputHuman)
	}
	mode, err := controlclient.ParseOutputMode(rawOutput)
	if err != nil {
		return executionOptions{}, err
	}
	if input.JSON {
		if input.OutputSet && mode != controlclient.OutputJSON {
			return executionOptions{}, controlclient.ErrInvalidInput
		}
		mode = controlclient.OutputJSON
	}
	return executionOptions{DataDir: input.DataDir, Output: mode}, nil
}
