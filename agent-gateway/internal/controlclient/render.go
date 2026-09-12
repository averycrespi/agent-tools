package controlclient

import (
	"fmt"
	"io"
	"strings"
)

const MaxTerminalPathBytes = 512

type Renderer struct {
	mode   OutputMode
	stdout io.Writer
	stderr io.Writer
}

func NewRenderer(mode OutputMode, stdout, stderr io.Writer) (Renderer, error) {
	if stdout == nil || stderr == nil {
		return Renderer{}, ErrInvalidInput
	}
	if mode == OutputTable {
		mode = OutputHuman
	}
	if mode != OutputHuman && mode != OutputJSON {
		return Renderer{}, ErrInvalidInput
	}
	return Renderer{mode: mode, stdout: stdout, stderr: stderr}, nil
}

func (renderer Renderer) WriteFiniteSuccess(jsonBody []byte, human string) error {
	if !renderer.valid() {
		return ErrInvalidInput
	}
	if renderer.mode == OutputJSON {
		return WriteSuccess(renderer.stdout, OutputJSON, jsonBody, Table{})
	}
	return writeHumanResult(renderer.stdout, human)
}

func (renderer Renderer) WriteProblem(problem *Problem) error {
	if !renderer.valid() {
		return ErrInvalidInput
	}
	return WriteFailure(renderer.stderr, renderer.mode, problem)
}

func (renderer Renderer) valid() bool {
	return renderer.stdout != nil && renderer.stderr != nil && (renderer.mode == OutputHuman || renderer.mode == OutputJSON)
}

type ServePhases struct {
	renderer     Renderer
	acknowledged bool
}

func NewServePhases(renderer Renderer) *ServePhases {
	return &ServePhases{renderer: renderer}
}

func (phases *ServePhases) Acknowledged() bool {
	return phases != nil && phases.acknowledged
}

func (phases *ServePhases) Acknowledge(jsonBody []byte, human string) error {
	if phases == nil || phases.acknowledged {
		return ErrInvalidInput
	}
	if err := phases.renderer.WriteFiniteSuccess(jsonBody, human); err != nil {
		return err
	}
	phases.acknowledged = true
	return nil
}

func (phases *ServePhases) WriteProblem(problem *Problem) error {
	if phases == nil {
		return ErrInvalidInput
	}
	return phases.renderer.WriteProblem(problem)
}

func TerminalSafePath(path string) string {
	rendered := terminalSafe(path)
	if len(rendered) <= MaxTerminalPathBytes {
		return rendered
	}
	var result strings.Builder
	for _, character := range path {
		token := terminalToken(character)
		if result.Len()+len(token) > MaxTerminalPathBytes-3 {
			result.WriteString("...")
			return result.String()
		}
		result.WriteString(token)
	}
	return result.String()
}

func writeHumanResult(writer io.Writer, value string) error {
	if value == "" {
		return ErrInvalidInput
	}
	value = strings.TrimSuffix(value, "\n")
	for _, line := range strings.Split(value, "\n") {
		if _, err := io.WriteString(writer, terminalSafe(line)+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func terminalToken(character rune) string {
	switch character {
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	default:
		if character < 0x20 || character == 0x7f {
			return fmt.Sprintf(`\u%04x`, character)
		}
		return string(character)
	}
}
