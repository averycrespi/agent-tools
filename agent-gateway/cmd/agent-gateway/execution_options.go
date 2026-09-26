package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

const (
	maxRenderedStartCommandBytes = 16 * 1024
	maxOnlineStartCommandBytes   = 400
)

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
		return "agent-gateway serve", nil
	}
	return renderPathFlagCommand("agent-gateway serve", "--data-dir", "data_dir", dataDir)
}

func renderOnlineServeCommand(address, dataDir string, includeDataDir bool) (string, error) {
	authority, err := controlclient.ListenAuthority(address)
	if err != nil {
		return "", err
	}
	command := "agent-gateway serve"
	if address != controlclient.DefaultAddress {
		command += " --listen " + authority
	}
	if includeDataDir {
		command, err = renderPathFlagCommand(command, "--data-dir", "data_dir", dataDir)
		if err != nil {
			return "", err
		}
	}
	if len(command) > maxOnlineStartCommandBytes {
		return "", controlclient.ErrInvalidInput
	}
	return command, nil
}

func renderBearerCommand(command, path string) (string, error) {
	return renderPathFlagCommand(command, "--admin-bearer-file", "bearer_file", path)
}

func renderInstallationCommand(command, root string) (string, error) {
	if defaults, err := gatewaypaths.Resolve(""); err == nil && defaults.Root == root {
		return command, nil
	}
	return renderPathFlagCommand(command, "--data-dir", "data_dir", root)
}

func renderPathFlagCommand(command, flag, variable, path string) (string, error) {
	if command == "" || flag == "" || variable == "" || path == "" || strings.ContainsAny(command+flag+variable, "\r\n\t") {
		return "", controlclient.ErrInvalidInput
	}
	// Ordinary paths need only shell quoting. Keep byte encoding for control
	// characters and invalid UTF-8 so suggestions never inject terminal controls.
	if utf8.ValidString(path) && strings.IndexFunc(path, func(r rune) bool { return !unicode.IsPrint(r) }) == -1 {
		rendered := command + " " + flag + " '" + strings.ReplaceAll(path, "'", `'"'"'`) + "'"
		if len(rendered) > maxRenderedStartCommandBytes {
			return "", controlclient.ErrInvalidInput
		}
		return rendered, nil
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
	return rendered, nil
}
