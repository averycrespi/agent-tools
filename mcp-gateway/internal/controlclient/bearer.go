package controlclient

import (
	"errors"
	"io"
	"os"
	"regexp"

	term "github.com/charmbracelet/x/term"
)

const maxBearerInputBytes = 128

var (
	ErrBearerSource  = errors.New("admin bearer source is unavailable")
	ErrInvalidBearer = errors.New("admin bearer is invalid")
)

var adminBearerPattern = regexp.MustCompile(`^mgw_admin_[A-Za-z0-9_-]{43}$`)

type PasswordPrompt interface {
	ReadPassword(string) ([]byte, error)
}

type BearerOptions struct {
	FilePath      string
	ReadStdin     bool
	Stdin         io.Reader
	Prompt        PasswordPrompt
	InputFilePath string
}

func AcquireAdminBearer(options BearerOptions) (string, error) {
	if options.FilePath != "" && options.ReadStdin || options.ReadStdin && options.InputFilePath == "-" {
		return "", ErrBearerSource
	}
	var (
		contents []byte
		err      error
	)
	switch {
	case options.FilePath != "":
		return readBearerFile(options.FilePath, os.Geteuid())
	case options.ReadStdin:
		if options.Stdin == nil {
			return "", ErrBearerSource
		}
		contents, err = readBoundedBearer(options.Stdin)
	case options.Prompt != nil:
		contents, err = options.Prompt.ReadPassword("Admin bearer: ")
	default:
		contents, err = terminalPasswordPrompt{}.ReadPassword("Admin bearer: ")
	}
	if err != nil {
		return "", ErrBearerSource
	}
	return validateBearerBytes(contents)
}

func readBoundedBearer(reader io.Reader) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, maxBearerInputBytes+1))
	if err != nil || len(contents) > maxBearerInputBytes {
		return nil, ErrInvalidBearer
	}
	if len(contents) > 0 && contents[len(contents)-1] == '\n' {
		contents = contents[:len(contents)-1]
	}
	return contents, nil
}

func validateBearerBytes(contents []byte) (string, error) {
	defer clear(contents)
	if !adminBearerPattern.Match(contents) {
		return "", ErrInvalidBearer
	}
	return string(contents), nil
}

type terminalPasswordPrompt struct{}

func (terminalPasswordPrompt) ReadPassword(message string) ([]byte, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = terminal.Close() }()
	if _, err := io.WriteString(terminal, message); err != nil {
		return nil, err
	}
	contents, err := term.ReadPassword(terminal.Fd())
	_, newlineErr := io.WriteString(terminal, "\n")
	if err != nil {
		return nil, err
	}
	if newlineErr != nil {
		clear(contents)
		return nil, newlineErr
	}
	return contents, nil
}
