package mcpingress

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPWorkAndStreamLimitsRejectWithoutQueuing(t *testing.T) {
	t.Parallel()
	handler, boundary := newModernBoundary(t)
	started := make(chan struct{}, 32)
	releaseWork := make(chan struct{})
	handler.modern = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-releaseWork
		writer.WriteHeader(http.StatusNoContent)
	})

	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			request := modernRequest(http.MethodPost, modernList)
			request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
			boundary.ServeHTTP(httptest.NewRecorder(), request)
		}()
	}
	for range 32 {
		<-started
	}
	work, streams, _ := handler.Status()
	assert.Equal(t, int64(32), work.InUse)
	assert.True(t, work.Saturated)
	assert.Zero(t, streams.InUse)

	request := modernRequest(http.MethodPost, modernList)
	request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)

	for range 32 {
		handler.streams <- struct{}{}
	}
	close(releaseWork)
	group.Wait()
	streamBody := `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	request = modernRequest(http.MethodPost, streamBody)
	request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	work, streams, _ = handler.Status()
	assert.Zero(t, work.InUse)
	assert.True(t, streams.Saturated)
	for range 32 {
		<-handler.streams
	}
}
