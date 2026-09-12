package controlclient

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

const maxBearerInputBytes = 128

var (
	ErrBearerSource      = errors.New("admin bearer source is unavailable")
	ErrInvalidBearer     = errors.New("admin bearer is invalid")
	ErrBearerMissing     = errors.New("admin bearer file is missing")
	ErrBearerSymlink     = errors.New("admin bearer file is a symlink")
	ErrBearerNotRegular  = errors.New("admin bearer file is not regular")
	ErrBearerPermissions = errors.New("admin bearer file permissions are unsafe")
	ErrBearerOwner       = errors.New("admin bearer file has the wrong owner")
	ErrBearerUnreadable  = errors.New("admin bearer source is unreadable")
	ErrBearerOversized   = errors.New("admin bearer is oversized")
	ErrBearerMalformed   = errors.New("admin bearer is malformed")
	ErrBearerConflict    = errors.New("admin bearer sources conflict")
)

var adminBearerPattern = regexp.MustCompile(`^mgw_admin_[A-Za-z0-9_-]{43}$`)

type BearerOptions struct {
	FilePath      string
	ReadStdin     bool
	Stdin         io.Reader
	InputFilePath string
}

func AcquireAdminBearer(options BearerOptions) (string, error) {
	if options.FilePath != "" && options.ReadStdin || options.ReadStdin && options.InputFilePath == "-" {
		return "", bearerSourceError(ErrBearerConflict)
	}
	switch {
	case options.FilePath != "":
		return readBearerFile(options.FilePath, os.Geteuid())
	case options.ReadStdin:
		if options.Stdin == nil {
			return "", bearerSourceError(ErrBearerUnreadable)
		}
		contents, err := readBoundedBearer(options.Stdin)
		if err != nil {
			return "", err
		}
		return validateBearerBytes(contents)
	default:
		return "", bearerSourceError(ErrBearerMissing)
	}
}

func readBoundedBearer(reader io.Reader) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, maxBearerInputBytes+1))
	if err != nil {
		clear(contents)
		return nil, bearerSourceError(ErrBearerUnreadable)
	}
	if len(contents) > maxBearerInputBytes {
		clear(contents)
		return nil, invalidBearerError(ErrBearerOversized)
	}
	if len(contents) > 0 && contents[len(contents)-1] == '\n' {
		contents = contents[:len(contents)-1]
	}
	return contents, nil
}

func validateBearerBytes(contents []byte) (string, error) {
	defer clear(contents)
	if !adminBearerPattern.Match(contents) {
		return "", invalidBearerError(ErrBearerMalformed)
	}
	return string(contents), nil
}

func ProjectBearerProblem(err error, path string) *Problem {
	label := "the selected administrator bearer"
	if path != "" {
		safePath := TerminalSafePath(path)
		if len(safePath) <= 256 {
			label = fmt.Sprintf("the administrator bearer file %q", safePath)
		} else {
			label = "the selected administrator bearer file"
		}
	}

	var online *OnlineError
	if errors.As(err, &online) && online.Status != nil && *online.Status == 401 {
		status := *online.Status
		return &Problem{Status: &status, Code: "client_bearer_rejected", Title: fmt.Sprintf("The Gateway rejected %s. Select the current owner-only bearer file and try again.", label), Exit: 3}
	}

	switch {
	case errors.Is(err, ErrBearerMissing):
		return &Problem{Code: "client_bearer_missing", Title: fmt.Sprintf("%s does not exist. Run mcp-gateway initialize or select an existing owner-only bearer file.", capitalize(label)), Exit: 2}
	case errors.Is(err, ErrBearerSymlink):
		return &Problem{Code: "client_bearer_symlink", Title: fmt.Sprintf("%s is a symlink. Select a regular owner-only bearer file instead.", capitalize(label)), Exit: 2}
	case errors.Is(err, ErrBearerNotRegular):
		return &Problem{Code: "client_bearer_not_regular", Title: fmt.Sprintf("%s is not a regular file. Select a regular owner-only bearer file.", capitalize(label)), Exit: 2}
	case errors.Is(err, ErrBearerPermissions):
		return &Problem{Code: "client_bearer_permissions", Title: fmt.Sprintf("%s must have permissions 0400 or 0600. Fix its permissions and try again.", capitalize(label)), Exit: 2}
	case errors.Is(err, ErrBearerOwner):
		return &Problem{Code: "client_bearer_owner", Title: fmt.Sprintf("%s is not owned by the current user. Select a current-user-owned bearer file.", capitalize(label)), Exit: 2}
	case errors.Is(err, ErrBearerUnreadable):
		return &Problem{Code: "client_bearer_unreadable", Title: fmt.Sprintf("%s could not be read. Check its path and owner-read permission.", capitalize(label)), Exit: 2}
	case errors.Is(err, ErrBearerOversized):
		return &Problem{Code: "client_bearer_oversized", Title: fmt.Sprintf("%s is too large. Select the exact bearer file created by mcp-gateway.", capitalize(label)), Exit: 2}
	case errors.Is(err, ErrBearerMalformed):
		return &Problem{Code: "client_bearer_malformed", Title: fmt.Sprintf("%s is malformed. Select the exact bearer file created by mcp-gateway.", capitalize(label)), Exit: 2}
	case errors.Is(err, ErrBearerConflict):
		return &Problem{Code: "client_bearer_source_conflict", Title: "Choose exactly one administrator bearer source: a file or standard input.", Exit: 2}
	default:
		return &Problem{Code: "client_bearer_unreadable", Title: "The selected administrator bearer could not be read. Check its source and try again.", Exit: 2}
	}
}

func isBearerAcquisitionError(err error) bool {
	return errors.Is(err, ErrBearerMissing) || errors.Is(err, ErrBearerSymlink) || errors.Is(err, ErrBearerNotRegular) ||
		errors.Is(err, ErrBearerPermissions) || errors.Is(err, ErrBearerOwner) || errors.Is(err, ErrBearerUnreadable) ||
		errors.Is(err, ErrBearerOversized) || errors.Is(err, ErrBearerMalformed) || errors.Is(err, ErrBearerConflict)
}

func bearerSourceError(kind error) error {
	return errors.Join(ErrBearerSource, kind)
}

func invalidBearerError(kind error) error {
	return errors.Join(ErrInvalidBearer, kind)
}

func capitalize(value string) string {
	if value == "" {
		return value
	}
	return "T" + value[1:]
}
