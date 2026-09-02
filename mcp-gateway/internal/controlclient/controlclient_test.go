package controlclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlTransport(t *testing.T) {
	t.Run("canonical loopback addresses", func(t *testing.T) {
		for _, address := range []string{DefaultAddress, "http://127.0.0.1:1", "http://127.255.254.253:65535"} {
			client, err := New(address, TransportOptions{})
			require.NoError(t, err, address)
			assert.Equal(t, address, client.Address())
		}
		for _, address := range []string{
			"", "https://127.0.0.1:8210", "http://localhost:8210", "http://127.0.0.1", "http://127.0.0.1:0",
			"http://127.0.0.1:65536", "http://127.00.0.1:8210", "http://127.0.0.01:8210", "http://127.0.0.1:08210",
			"http://127.0.0.1:8210/", "http://127.0.0.1:8210/path", "http://127.0.0.1:8210?x=1", "http://127.0.0.1:8210#x",
			"http://user@127.0.0.1:8210", "http://[::1]:8210", "http://2130706433:8210", " http://127.0.0.1:8210",
			"http://127%2e0%2e0%2e1:8210", "http://128.0.0.1:8210",
		} {
			_, err := New(address, TransportOptions{})
			assert.ErrorIs(t, err, ErrInvalidAddress, address)
		}
	})

	t.Run("bounded JSON response and no cookie persistence", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			requests++
			assert.Empty(t, request.Header.Get("Cookie"))
			response.Header().Set("Set-Cookie", "unsafe=persisted")
			_, _ = io.WriteString(response, `{"ok":true}`)
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, TransportOptions{})
		for range 2 {
			result, err := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/api/v1/status"})
			require.NoError(t, err)
			assert.JSONEq(t, `{"ok":true}`, string(result.Body))
		}
		assert.Equal(t, 2, requests)
		_, err := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/api/v1/status", Header: http.Header{"Cookie": {"unsafe=caller"}}})
		assert.ErrorIs(t, err, ErrInvalidRequest)
		assert.Equal(t, 2, requests)

		oversized := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(response, `"`+strings.Repeat("x", MaxResponseBytes)+`"`)
		}))
		defer oversized.Close()
		_, err = newTestClient(t, oversized.URL, TransportOptions{}).Do(context.Background(), Request{Method: http.MethodGet, Path: "/api/v1/status"})
		assert.ErrorIs(t, err, ErrResponseInvalid)

		deep := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(response, strings.Repeat("[", MaxJSONDepth+1)+"0"+strings.Repeat("]", MaxJSONDepth+1))
		}))
		defer deep.Close()
		_, err = newTestClient(t, deep.URL, TransportOptions{}).Do(context.Background(), Request{Method: http.MethodGet, Path: "/api/v1/status"})
		assert.ErrorIs(t, err, ErrResponseInvalid)
	})

	t.Run("redirects never follow", func(t *testing.T) {
		followed := false
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			http.Redirect(response, &http.Request{}, target.URL, http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		_, err := newTestClient(t, server.URL, TransportOptions{}).Do(context.Background(), Request{Method: http.MethodPost, Path: "/api/v1/backups", Body: []byte(`{}`)})
		assert.ErrorIs(t, err, ErrRedirect)
		assert.Equal(t, HandoffPossible, FailureHandoff(err))
		assert.False(t, followed)
	})

	t.Run("handoff and deadlines are explicit", func(t *testing.T) {
		canary := strings.Repeat("sensitive dial detail ", 40)
		client := newTestClient(t, DefaultAddress, TransportOptions{
			DialContext:    func(context.Context, string, string) (net.Conn, error) { return nil, errors.New(canary) },
			RequestTimeout: 100 * time.Millisecond,
		})
		_, err := client.Do(context.Background(), Request{Method: http.MethodPost, Path: "/api/v1/backups", Body: []byte(`{}`)})
		assert.ErrorIs(t, err, ErrTransport)
		assert.Equal(t, HandoffNone, FailureHandoff(err))
		assert.NotContains(t, err.Error(), canary)
		assert.LessOrEqual(t, len(err.Error()), MaxErrorTextBytes)

		received := make(chan struct{}, 1)
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			received <- struct{}{}
			time.Sleep(100 * time.Millisecond)
			_, _ = io.WriteString(response, `{}`)
		}))
		defer server.Close()
		client = newTestClient(t, server.URL, TransportOptions{RequestTimeout: 20 * time.Millisecond})
		_, err = client.Do(context.Background(), Request{Method: http.MethodPost, Path: "/api/v1/backups", Body: []byte(`{}`)})
		assert.ErrorIs(t, err, ErrTransport)
		assert.Equal(t, HandoffPossible, FailureHandoff(err))
		select {
		case <-received:
		default:
			t.Fatal("server did not receive handed-off request")
		}
	})

	t.Run("bearer sources are typed closed and bounded", func(t *testing.T) {
		root := t.TempDir()
		bearer := "mgw_admin_" + strings.Repeat("a", 43)
		file := filepath.Join(root, "bearer")
		require.NoError(t, os.WriteFile(file, []byte(bearer+"\n"), 0o600))
		actual, err := AcquireAdminBearer(BearerOptions{FilePath: file})
		require.NoError(t, err)
		assert.Equal(t, bearer, actual)

		_, err = AcquireAdminBearer(BearerOptions{FilePath: filepath.Join(root, "missing")})
		assert.ErrorIs(t, err, ErrBearerMissing)
		require.NoError(t, os.Chmod(file, 0o400))
		actual, err = AcquireAdminBearer(BearerOptions{FilePath: file})
		require.NoError(t, err)
		assert.Equal(t, bearer, actual)
		require.NoError(t, os.Chmod(file, 0o000))
		_, err = AcquireAdminBearer(BearerOptions{FilePath: file})
		assert.ErrorIs(t, err, ErrBearerUnreadable)
		require.NoError(t, os.Chmod(file, 0o644))
		_, err = AcquireAdminBearer(BearerOptions{FilePath: file})
		assert.ErrorIs(t, err, ErrBearerPermissions)
		require.NoError(t, os.Chmod(file, 0o600))

		symlink := filepath.Join(root, "bearer-link")
		require.NoError(t, os.Symlink(file, symlink))
		_, err = AcquireAdminBearer(BearerOptions{FilePath: symlink})
		assert.ErrorIs(t, err, ErrBearerSymlink)
		_, err = AcquireAdminBearer(BearerOptions{FilePath: root})
		assert.ErrorIs(t, err, ErrBearerNotRegular)
		_, err = readBearerFile(file, os.Geteuid()+1)
		assert.ErrorIs(t, err, ErrBearerOwner)

		require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("x", maxBearerInputBytes+1)), 0o600))
		_, err = AcquireAdminBearer(BearerOptions{FilePath: file})
		assert.ErrorIs(t, err, ErrBearerOversized)
		require.NoError(t, os.WriteFile(file, []byte("not-a-bearer\n"), 0o600))
		_, err = AcquireAdminBearer(BearerOptions{FilePath: file})
		assert.ErrorIs(t, err, ErrBearerMalformed)

		actual, err = AcquireAdminBearer(BearerOptions{ReadStdin: true, Stdin: strings.NewReader(bearer + "\n")})
		require.NoError(t, err)
		assert.Equal(t, bearer, actual)
		_, err = AcquireAdminBearer(BearerOptions{ReadStdin: true, Stdin: strings.NewReader(strings.Repeat("x", maxBearerInputBytes+1))})
		assert.ErrorIs(t, err, ErrBearerOversized)
		_, err = AcquireAdminBearer(BearerOptions{ReadStdin: true, Stdin: errorReader{}})
		assert.ErrorIs(t, err, ErrBearerUnreadable)
		_, err = AcquireAdminBearer(BearerOptions{FilePath: file, ReadStdin: true, Stdin: strings.NewReader(bearer)})
		assert.ErrorIs(t, err, ErrBearerConflict)
		_, err = AcquireAdminBearer(BearerOptions{ReadStdin: true, InputFilePath: "-", Stdin: strings.NewReader(bearer)})
		assert.ErrorIs(t, err, ErrBearerConflict)
		_, err = AcquireAdminBearer(BearerOptions{})
		assert.ErrorIs(t, err, ErrBearerMissing)
	})

	t.Run("bearer failures project actionable safe problems", func(t *testing.T) {
		path := "/tmp/admin\x1b[31m"
		tests := []struct {
			err  error
			code string
			exit int
		}{
			{err: ErrBearerMissing, code: "client_bearer_missing", exit: 2},
			{err: ErrBearerSymlink, code: "client_bearer_symlink", exit: 2},
			{err: ErrBearerNotRegular, code: "client_bearer_not_regular", exit: 2},
			{err: ErrBearerPermissions, code: "client_bearer_permissions", exit: 2},
			{err: ErrBearerOwner, code: "client_bearer_owner", exit: 2},
			{err: ErrBearerUnreadable, code: "client_bearer_unreadable", exit: 2},
			{err: ErrBearerOversized, code: "client_bearer_oversized", exit: 2},
			{err: ErrBearerMalformed, code: "client_bearer_malformed", exit: 2},
			{err: ErrBearerConflict, code: "client_bearer_source_conflict", exit: 2},
			{err: &OnlineError{Status: intPointer(401), Code: "unauthorized", Title: "rejected", Exit: 3}, code: "client_bearer_rejected", exit: 3},
		}
		for _, test := range tests {
			problem := ProjectBearerProblem(test.err, path)
			assert.Equal(t, test.code, problem.Code)
			assert.Equal(t, test.exit, problem.ExitCode())
			assert.NotContains(t, problem.Title, "\x1b")
			if test.code != "client_bearer_source_conflict" {
				assert.Contains(t, problem.Title, `\u001b`)
			}
		}
	})
}

func TestControlTransportStageClassification(t *testing.T) {
	refusedClient := newTestClient(t, DefaultAddress, TransportOptions{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		},
	})
	_, err := refusedClient.Do(context.Background(), Request{Method: http.MethodGet, Path: "/api/v1/status"})
	require.Error(t, err)
	assert.True(t, FailureRefused(err))
	assert.Equal(t, &OnlineError{Code: "gateway_not_running", Title: "MCP Gateway is not running.", Exit: 9}, ClassifyRequestError(err, RequestPhaseRead))

	preHandoff := &Failure{kind: ErrTransport, handoff: HandoffNone}
	assert.Equal(t, &OnlineError{Code: "client_transport_failure", Title: "The Gateway could not be reached before request handoff.", Exit: 9}, ClassifyRequestError(preHandoff, RequestPhaseMutation))

	postHandoff := &Failure{kind: ErrTransport, handoff: HandoffPossible}
	assert.Equal(t, &OnlineError{Code: "client_outcome_uncertain", Title: "The request outcome is uncertain.", Exit: 8, Uncertain: true}, ClassifyRequestError(postHandoff, RequestPhaseMutation))
	assert.Equal(t, &OnlineError{Code: "client_transport_failure", Title: "The read did not complete. This read is safe to repeat after checking Gateway availability.", Exit: 9}, ClassifyRequestError(postHandoff, RequestPhaseRead))
	assert.Equal(t, &OnlineError{Code: "client_transport_failure", Title: "The ETag preflight did not complete. The intended mutation was not submitted.", Exit: 9}, ClassifyRequestError(postHandoff, RequestPhasePreflight))

	truncated := &Failure{kind: ErrResponseInvalid, handoff: HandoffPossible}
	assert.Equal(t, &OnlineError{Code: "client_outcome_uncertain", Title: "The request outcome is uncertain.", Exit: 8, Uncertain: true}, ClassifyRequestError(truncated, RequestPhaseMutation))
	assert.Equal(t, "client_response_invalid", ClassifyRequestError(truncated, RequestPhaseRead).Code)
	assert.False(t, ClassifyRequestError(truncated, RequestPhaseRead).Uncertain)

	serviceUnavailable := EvaluateResponse(Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": {MediaTypeProblemJSON}}, Body: []byte(`{"status":503,"code":"storage_unavailable","title":"Storage is unavailable."}`)})
	require.NotNil(t, serviceUnavailable)
	assert.Equal(t, "storage_unavailable", serviceUnavailable.Code)
	assert.Equal(t, 7, serviceUnavailable.Exit)
	assert.False(t, serviceUnavailable.Uncertain)
	assert.Equal(t, "client_response_invalid", ClassifyRequestError(ErrResponseInvalid, RequestPhaseMutation).Code)
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("unreadable canary") }

func newTestClient(t *testing.T, rawURL string, options TransportOptions) *Client {
	t.Helper()
	address := strings.Replace(rawURL, "http://[::1]", "http://127.0.0.1", 1)
	client, err := New(address, options)
	require.NoError(t, err, fmt.Sprintf("test server address %q", address))
	return client
}
