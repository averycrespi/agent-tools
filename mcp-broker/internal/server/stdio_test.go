package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func TestNewStdioBackend_DrainsBackendStderr(t *testing.T) {
	if os.Getenv("MCP_BROKER_STDIO_HELPER") == "1" {
		runNoisyStdioHelper()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	backend, err := newStdioBackend(ctx, "noisy", config.ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestNewStdioBackend_DrainsBackendStderr"},
		Env:     map[string]string{"MCP_BROKER_STDIO_HELPER": "1"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NoError(t, backend.Close())
}

func runNoisyStdioHelper() {
	_, _ = os.Stderr.WriteString(strings.Repeat("stderr noise fills the pipe\n", 8192))

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return
	}

	var req struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return
	}

	_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"noisy","version":"0.1.0"}}}`+"\n", req.ID)
}
