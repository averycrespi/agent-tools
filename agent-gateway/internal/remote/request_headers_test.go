package remote

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestHeadersIncludeGeneratedFieldsBeforeHandoff(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "close", r.Header.Get("Connection"))
		assert.Equal(t, int64(2), r.ContentLength)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	endpoint, err := ParseEndpoint(server.URL+"/mcp", true)
	require.NoError(t, err)
	factory := New(Options{})
	header := http.Header{"User-Agent": {""}}
	for index := range 96 {
		header.Set(fmt.Sprintf("X-%d", index), "v")
	}
	request := Request{Endpoint: endpoint, Method: http.MethodPost, Header: header, Body: []byte(`{}`), MaxBody: 1024}
	_, err = factory.Exchange(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())
	header.Set("X-Extra", "v")
	handoff := false
	request.BeforeRoundTrip = func() { handoff = true }
	_, err = factory.Exchange(t.Context(), request)
	assert.ErrorIs(t, err, ErrResponseLimit)
	assert.False(t, handoff)
	assert.Equal(t, int32(1), calls.Load())

	header = http.Header{"User-Agent": {""}}
	for index := range 3 {
		header.Set(fmt.Sprintf("X-%d", index), strings.Repeat("v", 8192))
	}
	// The reserved headroom includes even the suppressed User-Agent name.
	generatedBytes := len("User-AgentHostConnectioncloseContent-Length2") + len(endpoint.url.Host)
	remaining := 32768 - generatedBytes - 3*(len("X-0")+8192) - len("X-Last")
	header.Set("X-Last", strings.Repeat("v", remaining))
	request.Header = header
	request.BeforeRoundTrip = nil
	_, err = factory.Exchange(t.Context(), request)
	require.NoError(t, err)
	header.Set("X-Last", strings.Repeat("v", remaining+1))
	request.BeforeRoundTrip = func() { handoff = true }
	_, err = factory.Exchange(t.Context(), request)
	assert.ErrorIs(t, err, ErrResponseLimit)
	assert.False(t, handoff)
	assert.Equal(t, int32(2), calls.Load())
}
