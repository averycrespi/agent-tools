package pi

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type writeCloser struct{ bytes.Buffer }

func (w *writeCloser) Close() error { return nil }

func TestClientCommands(t *testing.T) {
	stdin := &writeCloser{}
	client := NewClient(stdin, bytes.NewBuffer(nil))
	require.NoError(t, client.Prompt("hello"))
	require.NoError(t, client.Steer("focus"))
	require.NoError(t, client.FollowUp("next"))
	require.NoError(t, client.Abort())
	assert.Contains(t, stdin.String(), `{"type":"prompt","prompt":"hello"}`)
	assert.Contains(t, stdin.String(), `{"type":"steer","message":"focus"}`)
	assert.Contains(t, stdin.String(), `{"type":"follow_up","message":"next"}`)
	assert.Contains(t, stdin.String(), `{"type":"abort"}`)
}

func TestClientNext(t *testing.T) {
	client := NewClient(&writeCloser{}, bytes.NewBufferString(`{"type":"agent_end"}`+"\n"))
	event, raw, err := client.Next()
	require.NoError(t, err)
	assert.Equal(t, "agent_end", event.Type)
	assert.Equal(t, `{"type":"agent_end"}`, string(raw))
	_, _, err = client.Next()
	assert.ErrorIs(t, err, io.EOF)
}

func TestExtensionUIClassification(t *testing.T) {
	blocking := Event{Type: "extension_ui_request", Method: "confirm"}
	fire := Event{Type: "extension_ui_request", Method: "notify"}
	assert.True(t, blocking.IsBlockingExtensionUI())
	assert.False(t, blocking.IsFireAndForgetExtensionUI())
	assert.False(t, fire.IsBlockingExtensionUI())
	assert.True(t, fire.IsFireAndForgetExtensionUI())
}
