package main

import (
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
)

const maxRenderedStartCommandBytes = 16 * 1024

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

func renderServeCommand(dataDir string, useDefault bool) (string, error) {
	if useDefault {
		return "mcp-gateway serve", nil
	}
	return renderPathFlagCommand("mcp-gateway serve", "--data-dir", "data_dir", dataDir)
}

func renderBearerCommand(command, path string) (string, error) {
	return renderPathFlagCommand(command, "--admin-bearer-file", "bearer_file", path)
}

func renderPathFlagCommand(command, flag, variable, path string) (string, error) {
	if command == "" || flag == "" || variable == "" || path == "" {
		return "", controlclient.ErrInvalidInput
	}
	var encoded strings.Builder
	for _, value := range []byte(path) {
		if value >= 0x20 && value <= 0x7e && value != '\\' {
			encoded.WriteByte(value)
		} else {
			_, _ = fmt.Fprintf(&encoded, `\%03o`, value)
		}
	}
	quoted := "'" + strings.ReplaceAll(encoded.String(), "'", `'"'"'`) + "'"
	rendered := variable + "=$(printf '%b_' " + quoted + "); " + variable + "=${" + variable + "%_}; " + command + " " + flag + " \"$" + variable + "\""
	if len(rendered) > maxRenderedStartCommandBytes {
		return "", controlclient.ErrInvalidInput
	}
	if strings.ContainsAny(command+flag+variable, "\r\n\t") {
		return "", controlclient.ErrInvalidInput
	}
	return rendered, nil
}
