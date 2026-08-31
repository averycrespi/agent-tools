package controlclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

const (
	MaxInputBytes          = 1 * 1024 * 1024
	MediaTypeJSON          = "application/json"
	MediaTypeProblemJSON   = "application/problem+json"
	maxCursorBytes         = 8 * 1024
	maxETagBytes           = 1024
	maxIdempotencyKeyBytes = 128
	maxProblemCodeBytes    = 128
	maxProblemTitleBytes   = 512
)

var ErrInvalidInput = errors.New("control input is invalid")

type InputOptions struct {
	Path           string
	Stdin          io.Reader
	AllowedMembers []string
}

func ReadJSONInput(options InputOptions) ([]byte, error) {
	if options.Path == "" {
		return nil, ErrInvalidInput
	}
	var (
		reader io.Reader
		file   *os.File
		err    error
	)
	if options.Path == "-" {
		if options.Stdin == nil {
			return nil, ErrInvalidInput
		}
		reader = options.Stdin
	} else {
		file, err = os.Open(options.Path)
		if err != nil {
			return nil, ErrInvalidInput
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	contents, err := io.ReadAll(io.LimitReader(reader, MaxInputBytes+1))
	if err != nil || len(contents) > MaxInputBytes {
		return nil, ErrInvalidInput
	}
	value, err := strictjson.ParseValue(contents, strictjson.Options{MaxBytes: MaxInputBytes, MaxDepth: MaxJSONDepth})
	if err != nil || value.Type != strictjson.ValueObject {
		return nil, ErrInvalidInput
	}
	allowed := make(map[string]struct{}, len(options.AllowedMembers))
	for _, member := range options.AllowedMembers {
		if member == "" {
			return nil, ErrInvalidInput
		}
		allowed[member] = struct{}{}
	}
	for _, member := range value.Object {
		if _, ok := allowed[member.Name]; !ok {
			return nil, ErrInvalidInput
		}
	}
	return contents, nil
}

type ListOptions struct {
	Limit          int
	Cursor         string
	Filters        map[string]string
	AllowedFilters []string
}

func BuildListPath(base string, options ListOptions) (string, error) {
	if !validRequestPath(base) || strings.Contains(base, "?") || options.Limit < 0 || options.Limit > 100 || len(options.Cursor) > maxCursorBytes || strings.ContainsAny(options.Cursor, "\r\n") {
		return "", ErrInvalidInput
	}
	allowed := make(map[string]struct{}, len(options.AllowedFilters))
	for _, name := range options.AllowedFilters {
		if name == "" {
			return "", ErrInvalidInput
		}
		allowed[name] = struct{}{}
	}
	query := make(url.Values)
	if options.Limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", options.Limit))
	}
	if options.Cursor != "" {
		query.Set("cursor", options.Cursor)
	}
	for name, value := range options.Filters {
		if _, ok := allowed[name]; !ok || value == "" || strings.ContainsAny(name+value, "\r\n") {
			return "", ErrInvalidInput
		}
		query.Set(name, value)
	}
	if len(query) == 0 {
		return base, nil
	}
	return base + "?" + query.Encode(), nil
}

type RequestMetadataOptions struct {
	Bearer         string
	ETag           string
	IdempotencyKey string
	JSONBody       bool
}

func RequestMetadata(options RequestMetadataOptions) (http.Header, error) {
	if !adminBearerPattern.MatchString(options.Bearer) || !validETag(options.ETag) || !validOptionalHeader(options.IdempotencyKey, maxIdempotencyKeyBytes) {
		return nil, ErrInvalidInput
	}
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+options.Bearer)
	if options.ETag != "" {
		header.Set("If-Match", options.ETag)
	}
	if options.IdempotencyKey != "" {
		header.Set("Idempotency-Key", options.IdempotencyKey)
	}
	if options.JSONBody {
		header.Set("Content-Type", MediaTypeJSON)
	}
	return header, nil
}

func validETag(value string) bool {
	if value == "" {
		return true
	}
	return len(value) <= maxETagBytes && len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' && !strings.Contains(value, ",") && validOptionalHeader(value, maxETagBytes)
}

func validOptionalHeader(value string, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

type OutputMode string

const (
	OutputHuman OutputMode = "human"
	OutputTable OutputMode = "table"
	OutputJSON  OutputMode = "json"
)

type Table struct {
	Headers    []string
	Rows       [][]string
	NextCursor *string
}

func ParseOutputMode(raw string) (OutputMode, error) {
	switch OutputMode(raw) {
	case OutputHuman, OutputTable:
		return OutputHuman, nil
	case OutputJSON:
		return OutputJSON, nil
	default:
		return "", ErrInvalidInput
	}
}

func WriteSuccess(writer io.Writer, mode OutputMode, body []byte, table Table) error {
	switch mode {
	case OutputJSON:
		if _, err := strictjson.ParseValue(body, strictjson.Options{MaxBytes: MaxResponseBytes, MaxDepth: MaxJSONDepth}); err != nil {
			return ErrResponseInvalid
		}
		if _, err := writer.Write(body); err != nil {
			return err
		}
		if body[len(body)-1] == '\n' {
			return nil
		}
		_, err := io.WriteString(writer, "\n")
		return err
	case OutputHuman, OutputTable:
		return writeTable(writer, table)
	default:
		return ErrInvalidInput
	}
}

func writeTable(writer io.Writer, table Table) error {
	for _, row := range table.Rows {
		if len(row) != len(table.Headers) {
			return ErrInvalidInput
		}
	}
	output := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	writeRow := func(row []string) error {
		for index, cell := range row {
			if index > 0 {
				if _, err := io.WriteString(output, "\t"); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(output, terminalSafe(cell)); err != nil {
				return err
			}
		}
		_, err := io.WriteString(output, "\n")
		return err
	}
	if len(table.Headers) > 0 {
		if err := writeRow(table.Headers); err != nil {
			return err
		}
	}
	for _, row := range table.Rows {
		if err := writeRow(row); err != nil {
			return err
		}
	}
	if table.NextCursor != nil {
		if _, err := io.WriteString(output, "\n"); err != nil {
			return err
		}
		if err := writeRow([]string{"NEXT_CURSOR", *table.NextCursor}); err != nil {
			return err
		}
	}
	return output.Flush()
}

func terminalSafe(value string) string {
	var result strings.Builder
	for _, character := range value {
		switch character {
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			if character < 0x20 || character == 0x7f {
				_, _ = fmt.Fprintf(&result, `\u%04x`, character)
			} else {
				result.WriteRune(character)
			}
		}
	}
	return result.String()
}

type OnlineError struct {
	Status    *int   `json:"status"`
	Code      string `json:"code"`
	Title     string `json:"title"`
	Exit      int    `json:"exit_code"`
	Uncertain bool   `json:"uncertain"`
}

type Problem = OnlineError

func (failure *OnlineError) Error() string { return failure.Title }
func (failure *OnlineError) ExitCode() int { return failure.Exit }

type problemEnvelope struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Title  string `json:"title"`
}

func DecodeResponse(body []byte, destination any) error {
	if destination == nil {
		return ErrResponseInvalid
	}
	if err := strictjson.Decode(body, destination, strictjson.Options{MaxBytes: MaxResponseBytes, MaxDepth: MaxJSONDepth, RejectUnknownMembers: true}); err != nil {
		return ErrResponseInvalid
	}
	return nil
}

func EvaluateResponse(response Response) *OnlineError {
	contentType := response.Header.Get("Content-Type")
	if response.StatusCode >= 200 && response.StatusCode <= 299 {
		if response.StatusCode == http.StatusNoContent && len(response.Body) == 0 {
			return nil
		}
		if (response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated) || contentType != MediaTypeJSON || len(response.Body) == 0 {
			return responseInvalid()
		}
		return nil
	}
	exit, ok := statusExit(response.StatusCode)
	if !ok || contentType != MediaTypeProblemJSON {
		return responseInvalid()
	}
	var problem problemEnvelope
	if err := strictjson.Decode(response.Body, &problem, strictjson.Options{MaxBytes: MaxResponseBytes, MaxDepth: MaxJSONDepth, RejectUnknownMembers: true}); err != nil || problem.Status != response.StatusCode || !validProblemCode(problem.Code) || !validProblemTitle(problem.Title) {
		return responseInvalid()
	}
	status := problem.Status
	return &OnlineError{Status: &status, Code: problem.Code, Title: problem.Title, Exit: exit}
}

type RequestPhase uint8

const (
	RequestPhaseMutation RequestPhase = iota
	RequestPhaseRead
	RequestPhasePreflight
)

func ClassifyClientError(err error) *OnlineError {
	return ClassifyRequestError(err, RequestPhaseMutation)
}

func ClassifyRequestError(err error, phase RequestPhase) *OnlineError {
	switch {
	case errors.Is(err, ErrTransport):
		if FailureRefused(err) {
			return &OnlineError{Code: "gateway_not_running", Title: "MCP Gateway is not running.", Exit: 9}
		}
		if phase == RequestPhaseRead {
			return &OnlineError{Code: "client_transport_failure", Title: "The read did not complete. This read is safe to repeat after checking Gateway availability.", Exit: 9}
		}
		if phase == RequestPhasePreflight {
			return &OnlineError{Code: "client_transport_failure", Title: "The ETag preflight did not complete. The intended mutation was not submitted.", Exit: 9}
		}
		if FailureHandoff(err) == HandoffPossible {
			return &OnlineError{Code: "client_outcome_uncertain", Title: "The request outcome is uncertain.", Exit: 8, Uncertain: true}
		}
		return &OnlineError{Code: "client_transport_failure", Title: "The Gateway could not be reached before request handoff.", Exit: 9}
	case errors.Is(err, ErrRedirect), errors.Is(err, ErrResponseInvalid):
		if phase == RequestPhaseMutation && FailureHandoff(err) == HandoffPossible {
			return &OnlineError{Code: "client_outcome_uncertain", Title: "The request outcome is uncertain.", Exit: 8, Uncertain: true}
		}
		return responseInvalid()
	case errors.Is(err, ErrSecretSinkUnavailable), errors.Is(err, ErrSecretLost):
		return NewSecretSinkError("The one-time value could not be published.")
	case isBearerAcquisitionError(err):
		return ProjectBearerProblem(err, "")
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrInvalidAddress), errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrInvalidBearer), errors.Is(err, ErrBearerSource), errors.Is(err, ErrConfirmationRequired):
		return &OnlineError{Code: "client_invalid_input", Title: "The command input is invalid.", Exit: 2}
	default:
		return responseInvalid()
	}
}

func NewInputError(title string) *OnlineError {
	if !validProblemTitle(title) {
		title = "The command input is invalid."
	}
	return &OnlineError{Code: "client_invalid_input", Title: title, Exit: 2}
}

func WriteFailure(writer io.Writer, mode OutputMode, failure *OnlineError) error {
	if failure == nil || failure.Exit < 2 || failure.Exit > 10 || !validProblemCode(failure.Code) || !validProblemTitle(failure.Title) {
		return ErrInvalidInput
	}
	switch mode {
	case OutputJSON:
		encoded, err := json.Marshal(failure)
		if err != nil {
			return err
		}
		encoded = append(encoded, '\n')
		_, err = writer.Write(encoded)
		return err
	case OutputHuman, OutputTable:
		_, err := io.WriteString(writer, terminalSafe(failure.Title)+"\n")
		return err
	default:
		return ErrInvalidInput
	}
}

func statusExit(status int) (int, bool) {
	switch status {
	case 400, 405, 413, 415, 421:
		return 2, true
	case 401, 403:
		return 3, true
	case 404:
		return 4, true
	case 409, 412, 428:
		return 5, true
	case 429:
		return 6, true
	case 503:
		return 7, true
	default:
		return 0, false
	}
}

func responseInvalid() *OnlineError {
	return &OnlineError{Code: "client_response_invalid", Title: "The Gateway response is invalid.", Exit: 10}
}

func validProblemCode(value string) bool {
	if value == "" || len(value) > maxProblemCodeBytes {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

func validProblemTitle(value string) bool {
	if value == "" || len(value) > maxProblemTitleBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
