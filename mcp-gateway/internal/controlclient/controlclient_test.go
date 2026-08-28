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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6ControlTransport(t *testing.T) {
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

	t.Run("bearer sources are closed and bounded", func(t *testing.T) {
		root := t.TempDir()
		bearer := "mgw_admin_" + strings.Repeat("a", 43)
		file := filepath.Join(root, "bearer")
		require.NoError(t, os.WriteFile(file, []byte(bearer+"\n"), 0o600))
		actual, err := AcquireAdminBearer(BearerOptions{FilePath: file})
		require.NoError(t, err)
		assert.Equal(t, bearer, actual)

		require.NoError(t, os.Chmod(file, 0o400))
		actual, err = AcquireAdminBearer(BearerOptions{FilePath: file})
		require.NoError(t, err)
		assert.Equal(t, bearer, actual)
		require.NoError(t, os.Chmod(file, 0o644))
		_, err = AcquireAdminBearer(BearerOptions{FilePath: file})
		assert.ErrorIs(t, err, ErrBearerSource)

		symlink := filepath.Join(root, "bearer-link")
		require.NoError(t, os.Symlink(file, symlink))
		_, err = AcquireAdminBearer(BearerOptions{FilePath: symlink})
		assert.ErrorIs(t, err, ErrBearerSource)
		_, err = readBearerFile(file, os.Geteuid()+1)
		assert.ErrorIs(t, err, ErrBearerSource)

		actual, err = AcquireAdminBearer(BearerOptions{ReadStdin: true, Stdin: strings.NewReader(bearer + "\n")})
		require.NoError(t, err)
		assert.Equal(t, bearer, actual)
		_, err = AcquireAdminBearer(BearerOptions{ReadStdin: true, Stdin: strings.NewReader(bearer + "\nextra")})
		assert.ErrorIs(t, err, ErrInvalidBearer)
		_, err = AcquireAdminBearer(BearerOptions{FilePath: file, ReadStdin: true, Stdin: strings.NewReader(bearer)})
		assert.ErrorIs(t, err, ErrBearerSource)
		_, err = AcquireAdminBearer(BearerOptions{ReadStdin: true, InputFilePath: "-", Stdin: strings.NewReader(bearer)})
		assert.ErrorIs(t, err, ErrBearerSource)

		prompt := &fakePrompt{value: []byte(bearer)}
		actual, err = AcquireAdminBearer(BearerOptions{Prompt: prompt})
		require.NoError(t, err)
		assert.Equal(t, bearer, actual)
		assert.Equal(t, "Admin bearer: ", prompt.message)
	})
}

type fakePrompt struct {
	message string
	value   []byte
}

func (prompt *fakePrompt) ReadPassword(message string) ([]byte, error) {
	prompt.message = message
	return append([]byte(nil), prompt.value...), nil
}

func newTestClient(t *testing.T, rawURL string, options TransportOptions) *Client {
	t.Helper()
	address := strings.Replace(rawURL, "http://[::1]", "http://127.0.0.1", 1)
	client, err := New(address, options)
	require.NoError(t, err, fmt.Sprintf("test server address %q", address))
	return client
}
