package control

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendServe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.sock")
	server, err := Listen(path)
	require.NoError(t, err)
	defer server.Close() //nolint:errcheck

	done := make(chan error, 1)
	go func() {
		done <- server.Serve(func(req Request) Response {
			assert.Equal(t, OpSteer, req.Operation)
			assert.Equal(t, "focus", req.Message)
			return Response{OK: true}
		})
	}()

	resp, err := Send(path, Request{Operation: OpSteer, Message: "focus"})
	require.NoError(t, err)
	assert.True(t, resp.OK)
	require.NoError(t, server.Close())
	<-done
}

func TestSendErrorResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.sock")
	server, err := Listen(path)
	require.NoError(t, err)
	defer server.Close() //nolint:errcheck

	go func() {
		_ = server.Serve(func(req Request) Response {
			return Response{OK: false, Error: "nope"}
		})
	}()

	_, err = Send(path, Request{Operation: OpStop})
	assert.ErrorContains(t, err, "nope")
}
