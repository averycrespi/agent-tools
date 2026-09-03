package controlclient

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlClientSharedContract(t *testing.T) {
	t.Run("strict one-document input", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "input.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"name":"value","count":1}`), 0o600))
		body, err := ReadJSONInput(InputOptions{Path: path, AllowedMembers: []string{"name", "count"}})
		require.NoError(t, err)
		assert.Equal(t, `{"name":"value","count":1}`, string(body))

		body, err = ReadJSONInput(InputOptions{Path: "-", Stdin: strings.NewReader(`{"name":"stdin"}`), AllowedMembers: []string{"name"}})
		require.NoError(t, err)
		assert.Equal(t, `{"name":"stdin"}`, string(body))
		for _, input := range []string{
			`{"name":"first","name":"second"}`,
			`{"unknown":true}`,
			`[]`,
			`{"name":"value"} {}`,
			strings.Repeat("[", MaxJSONDepth+1) + "0" + strings.Repeat("]", MaxJSONDepth+1),
		} {
			_, err := ReadJSONInput(InputOptions{Path: "-", Stdin: strings.NewReader(input), AllowedMembers: []string{"name"}})
			assert.ErrorIs(t, err, ErrInvalidInput, input)
		}
		_, err = ReadJSONInput(InputOptions{Path: "-", Stdin: strings.NewReader(strings.Repeat("x", MaxInputBytes+1))})
		assert.ErrorIs(t, err, ErrInvalidInput)
	})

	t.Run("one-page query and request metadata", func(t *testing.T) {
		path, err := BuildListPath("/api/v1/invocations", ListOptions{
			Limit: 25, Cursor: "cursor/value", Filters: map[string]string{"decision": "ALLOW", "principal_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
			AllowedFilters: []string{"principal_id", "decision"},
		})
		require.NoError(t, err)
		assert.Equal(t, "/api/v1/invocations?cursor=cursor%2Fvalue&decision=ALLOW&limit=25&principal_id=01ARZ3NDEKTSV4RRFFQ69G5FAV", path)
		_, err = BuildListPath("/api/v1/invocations", ListOptions{Limit: 101})
		assert.ErrorIs(t, err, ErrInvalidInput)
		_, err = BuildListPath("/api/v1/invocations", ListOptions{Filters: map[string]string{"invented": "value"}, AllowedFilters: []string{"decision"}})
		assert.ErrorIs(t, err, ErrInvalidInput)

		bearer := "mgw_admin_" + strings.Repeat("a", 43)
		header, err := RequestMetadata(RequestMetadataOptions{Bearer: bearer, ETag: `"server:7"`, IdempotencyKey: "intent-1", JSONBody: true})
		require.NoError(t, err)
		assert.Equal(t, "Bearer "+bearer, header.Get("Authorization"))
		assert.Equal(t, `"server:7"`, header.Get("If-Match"))
		assert.Equal(t, "intent-1", header.Get("Idempotency-Key"))
		assert.Equal(t, MediaTypeJSON, header.Get("Content-Type"))
		_, err = RequestMetadata(RequestMetadataOptions{Bearer: bearer, ETag: "bad\nvalue"})
		assert.ErrorIs(t, err, ErrInvalidInput)
	})

	t.Run("success output and safe tables", func(t *testing.T) {
		var output bytes.Buffer
		require.NoError(t, WriteSuccess(&output, OutputJSON, []byte(`{"exact":1}`), Table{}))
		assert.Equal(t, "{\"exact\":1}\n", output.String())
		output.Reset()
		nextCursor := "page/cursor"
		require.NoError(t, WriteSuccess(&output, OutputTable, nil, Table{Headers: []string{"NAME", "STATE"}, Rows: [][]string{{"unsafe\x1b[31m", "ready\nnow"}}, NextCursor: &nextCursor, Notes: []string{"Bearer published once."}}))
		assert.NotContains(t, output.String(), "\x1b")
		assert.Contains(t, output.String(), `unsafe\u001b[31m`)
		assert.Contains(t, output.String(), `ready\nnow`)
		assert.Contains(t, output.String(), "NEXT_CURSOR  page/cursor")
		assert.Contains(t, output.String(), "NOTE         Bearer published once.")
	})

	t.Run("problems and exits are closed", func(t *testing.T) {
		apiError := EvaluateResponse(Response{StatusCode: 412, Header: http.Header{"Content-Type": {MediaTypeProblemJSON}}, Body: []byte(`{"status":412,"code":"stale_revision","title":"The server revision is stale."}`)})
		require.Error(t, apiError)
		assert.Equal(t, 5, apiError.ExitCode())
		assert.False(t, apiError.Uncertain)

		legacyConfigurationError := EvaluateResponse(Response{StatusCode: 400, Header: http.Header{"Content-Type": {MediaTypeProblemJSON}}, Body: []byte(`{"status":400,"code":"invalid_server_configuration","title":"The server configuration is invalid."}`)})
		require.Error(t, legacyConfigurationError)
		assert.Nil(t, legacyConfigurationError.Context)

		configurationError := EvaluateResponse(Response{StatusCode: 400, Header: http.Header{"Content-Type": {MediaTypeProblemJSON}}, Body: []byte(`{"status":400,"code":"invalid_server_configuration","title":"The server configuration is invalid.","context":{"field":"transport.working_directory","rule":"canonical_absolute_path"}}`)})
		require.Error(t, configurationError)
		require.NotNil(t, configurationError.Context)
		assert.Equal(t, "transport.working_directory", configurationError.Context.Field)
		var human bytes.Buffer
		require.NoError(t, WriteFailure(&human, OutputHuman, configurationError))
		assert.Equal(t, "The server configuration is invalid. [transport.working_directory: canonical_absolute_path]\n", human.String())

		for status, exitCode := range map[int]int{400: 2, 401: 3, 403: 3, 404: 4, 409: 5, 413: 2, 415: 2, 421: 2, 428: 5, 429: 6, 503: 7} {
			body := []byte(`{"status":` + strconv.Itoa(status) + `,"code":"code","title":"Safe."}`)
			problem := EvaluateResponse(Response{StatusCode: status, Header: http.Header{"Content-Type": {MediaTypeProblemJSON}}, Body: body})
			require.Error(t, problem, status)
			assert.Equal(t, exitCode, problem.ExitCode(), status)
		}
		assert.Nil(t, EvaluateResponse(Response{StatusCode: 200, Header: http.Header{"Content-Type": {MediaTypeJSON}}, Body: []byte(`{"ok":true}`)}))
		assert.Equal(t, 10, EvaluateResponse(Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: []byte(`{"ok":true}`)}).ExitCode())
		assert.Equal(t, 10, EvaluateResponse(Response{StatusCode: 409, Header: http.Header{"Content-Type": {MediaTypeProblemJSON}}, Body: []byte(`{"status":409,"code":"conflict","title":"x","extra":true}`)}).ExitCode())

		pre := ClassifyClientError(&Failure{kind: ErrTransport, handoff: HandoffNone})
		assert.Equal(t, 9, pre.ExitCode())
		assert.False(t, pre.Uncertain)
		post := ClassifyClientError(&Failure{kind: ErrTransport, handoff: HandoffPossible})
		assert.Equal(t, 8, post.ExitCode())
		assert.True(t, post.Uncertain)
		assert.Equal(t, 10, ClassifyClientError(ErrResponseInvalid).ExitCode())
	})

	t.Run("failure output is exact and bounded", func(t *testing.T) {
		failure := &OnlineError{Status: intPointer(409), Code: "conflict", Title: "The request conflicts with current state.", Exit: 5}
		var output bytes.Buffer
		require.NoError(t, WriteFailure(&output, OutputJSON, failure))
		assert.Equal(t, "{\"status\":409,\"code\":\"conflict\",\"title\":\"The request conflicts with current state.\",\"exit_code\":5,\"uncertain\":false}\n", output.String())
		assert.Equal(t, 5, failure.ExitCode())
		output.Reset()
		require.NoError(t, WriteFailure(&output, OutputTable, failure))
		assert.Equal(t, "The request conflicts with current state.\n", output.String())
	})
}

func TestCLIRenderPrimitives(t *testing.T) {
	t.Run("human aliases and json remain closed", func(t *testing.T) {
		for _, raw := range []string{"human", "table"} {
			mode, err := ParseOutputMode(raw)
			require.NoError(t, err)
			assert.Equal(t, OutputHuman, mode)
		}
		mode, err := ParseOutputMode("json")
		require.NoError(t, err)
		assert.Equal(t, OutputJSON, mode)
		_, err = ParseOutputMode("yaml")
		assert.ErrorIs(t, err, ErrInvalidInput)
	})

	t.Run("finite output uses exact streams", func(t *testing.T) {
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		renderer, err := NewRenderer(OutputHuman, stdout, stderr)
		require.NoError(t, err)
		require.NoError(t, renderer.WriteFiniteSuccess([]byte(`{"ok":true}`), "Initialized at /safe/\x1b[31m"))
		assert.Equal(t, `Initialized at /safe/\u001b[31m`+"\n", stdout.String())
		assert.Empty(t, stderr.String())

		problem := &OnlineError{Code: "client_invalid_input", Title: "Choose a valid path.", Exit: 2}
		require.NoError(t, renderer.WriteProblem(problem))
		assert.Equal(t, "Choose a valid path.\n", stderr.String())

		stdout.Reset()
		stderr.Reset()
		renderer, err = NewRenderer(OutputJSON, stdout, stderr)
		require.NoError(t, err)
		require.NoError(t, renderer.WriteFiniteSuccess([]byte(`{"ok":true}`), "ignored"))
		assert.Equal(t, "{\"ok\":true}\n", stdout.String())
		require.NoError(t, renderer.WriteProblem(problem))
		assert.Equal(t, "{\"status\":null,\"code\":\"client_invalid_input\",\"title\":\"Choose a valid path.\",\"exit_code\":2,\"uncertain\":false}\n", stderr.String())
	})

	t.Run("paths are escaped and bounded", func(t *testing.T) {
		safe := TerminalSafePath("/tmp/unsafe\x1b[31m\n" + strings.Repeat("x", 2048))
		assert.NotContains(t, safe, "\x1b")
		assert.NotContains(t, safe, "\n")
		assert.Contains(t, safe, `\u001b`)
		assert.Contains(t, safe, `\n`)
		assert.LessOrEqual(t, len(safe), MaxTerminalPathBytes)
		assert.True(t, strings.HasSuffix(safe, "..."))
	})

	t.Run("serve acknowledgement is singular and terminal problems stay on stderr", func(t *testing.T) {
		stdout := new(bytes.Buffer)
		stderr := new(bytes.Buffer)
		renderer, err := NewRenderer(OutputJSON, stdout, stderr)
		require.NoError(t, err)
		phases := NewServePhases(renderer)
		assert.False(t, phases.Acknowledged())
		require.NoError(t, phases.Acknowledge([]byte(`{"ok":true,"operation":"serve"}`), "Gateway started."))
		assert.True(t, phases.Acknowledged())
		assert.ErrorIs(t, phases.Acknowledge([]byte(`{"ok":true}`), "duplicate"), ErrInvalidInput)
		require.NoError(t, phases.WriteProblem(&OnlineError{Code: "storage_unavailable", Title: "The Gateway stopped unexpectedly.", Exit: 7}))
		assert.Equal(t, "{\"ok\":true,\"operation\":\"serve\"}\n", stdout.String())
		assert.Equal(t, "{\"status\":null,\"code\":\"storage_unavailable\",\"title\":\"The Gateway stopped unexpectedly.\",\"exit_code\":7,\"uncertain\":false}\n", stderr.String())
	})
}

func intPointer(value int) *int { return &value }
