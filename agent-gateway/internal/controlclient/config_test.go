package controlclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestConfigurationInputIndependentBoundAndStrictness(t *testing.T) {
	body := `{"spec":"` + strings.Repeat("x", MaxInputBytes) + `"}`
	options := InputOptions{Path: "-", Stdin: strings.NewReader(body), AllowedMembers: []string{"spec"}}
	_, err := ReadJSONInput(options)
	require.ErrorIs(t, err, ErrInvalidInput)
	options.Stdin = strings.NewReader(body)
	got, err := ReadConfigInput(options)
	require.NoError(t, err)
	require.Equal(t, body, string(got))
	for _, input := range []string{`{"spec":{},"spec":{}}`, `{"other":{}}`, `{"spec":{}}{}`, `[]`, strings.Repeat("[", MaxJSONDepth+1) + "0" + strings.Repeat("]", MaxJSONDepth+1)} {
		_, err = ReadConfigInput(InputOptions{Path: "-", Stdin: strings.NewReader(input), AllowedMembers: []string{"spec"}})
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	_, err = readJSONInput(InputOptions{Path: "-", Stdin: strings.NewReader(`{"spec":123}`), AllowedMembers: []string{"spec"}}, 4)
	require.ErrorIs(t, err, ErrInvalidInput)
}
func TestConfigurationTransportBoundsAreRouteScoped(t *testing.T) {
	body := []byte(`{"spec":"` + strings.Repeat("x", MaxResponseBytes) + `"}`)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		w.Header().Set("Content-Type", MediaTypeJSON)
		if r.Method == http.MethodPost {
			require.Equal(t, body, data)
			_, _ = io.WriteString(w, `{}`)
		} else {
			_, _ = w.Write(body)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, TransportOptions{})
	require.NoError(t, err)
	for _, path := range []string{contract.ConfigImportPath, contract.ConfigPreviewPath} {
		_, err = client.Do(context.Background(), Request{Method: http.MethodPost, Path: path, Body: body})
		require.NoError(t, err)
	}
	_, err = client.Do(context.Background(), Request{Method: http.MethodPost, Path: "/api/v2/principals", Body: body})
	require.ErrorIs(t, err, ErrInvalidRequest)
	require.Equal(t, 2, calls)
	response, err := client.Do(context.Background(), Request{Method: http.MethodGet, Path: contract.ConfigResourcePath + "mcp_server/example"})
	require.NoError(t, err)
	require.Equal(t, body, response.Body)
	_, err = client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/api/v2/principals"})
	require.ErrorIs(t, err, ErrResponseInvalid)
	require.Equal(t, 4, calls)
}
