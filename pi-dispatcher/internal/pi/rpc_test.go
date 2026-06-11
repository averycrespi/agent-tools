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
	require.NoError(t, client.Abort())
	assert.Contains(t, stdin.String(), `{"type":"prompt","message":"hello"}`)
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

func TestClientNextAcceptsCRLF(t *testing.T) {
	client := NewClient(&writeCloser{}, bytes.NewBufferString(`{"type":"agent_end"}`+"\r\n"))
	event, raw, err := client.Next()
	require.NoError(t, err)
	assert.Equal(t, "agent_end", event.Type)
	assert.Equal(t, `{"type":"agent_end"}`, string(raw))
}

func TestClientNextReadsLargeJSONLRecord(t *testing.T) {
	large := bytes.Repeat([]byte("x"), 11*1024*1024)
	input := append([]byte(`{"type":"message_update","data":{"text":"`), large...)
	input = append(input, []byte(`"}}`+"\n")...)
	client := NewClient(&writeCloser{}, bytes.NewReader(input))
	event, _, err := client.Next()
	require.NoError(t, err)
	assert.Equal(t, "message_update", event.Type)
}

func TestEventSessionFile(t *testing.T) {
	client := NewClient(&writeCloser{}, bytes.NewBufferString(`{"type":"response","command":"get_state","success":true,"data":{"sessionFile":"/tmp/session.json"}}`+"\n"))
	event, _, err := client.Next()
	require.NoError(t, err)
	assert.Equal(t, "response", event.Type)
	assert.Equal(t, "get_state", event.Command)
	assert.Equal(t, "/tmp/session.json", event.SessionFile())
}

func TestExtensionUIClassification(t *testing.T) {
	blocking := Event{Type: "extension_ui_request", Method: "confirm"}
	fire := Event{Type: "extension_ui_request", Method: "notify"}
	assert.True(t, blocking.IsBlockingExtensionUI())
	assert.False(t, blocking.IsFireAndForgetExtensionUI())
	assert.False(t, fire.IsBlockingExtensionUI())
	assert.True(t, fire.IsFireAndForgetExtensionUI())
}
