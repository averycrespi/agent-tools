package pi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

type Client struct {
	in  io.WriteCloser
	out *bufio.Reader
}

func NewClient(stdin io.WriteCloser, stdout io.Reader) *Client {
	return &Client{in: stdin, out: bufio.NewReader(stdout)}
}

func (c *Client) Prompt(text string) error { return c.send(command{Type: "prompt", Message: text}) }
func (c *Client) Steer(text string) error  { return c.send(command{Type: "steer", Message: text}) }
func (c *Client) FollowUp(text string) error {
	return c.send(command{Type: "follow_up", Message: text})
}
func (c *Client) Abort() error    { return c.send(command{Type: "abort"}) }
func (c *Client) GetState() error { return c.send(command{Type: "get_state"}) }

func (c *Client) ExtensionUIResponse(id string, cancelled bool, value any) error {
	return c.send(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": cancelled, "value": value})
}

func (c *Client) Next() (Event, []byte, error) {
	raw, err := c.out.ReadBytes('\n')
	if err != nil {
		if len(raw) == 0 {
			return Event{}, nil, err
		}
		if err != io.EOF {
			return Event{}, nil, err
		}
	}
	raw = append([]byte(nil), raw...)
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
	}
	if len(raw) > 0 && raw[len(raw)-1] == '\r' {
		raw = raw[:len(raw)-1]
	}
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return Event{}, raw, fmt.Errorf("parse Pi RPC event: %w", err)
	}
	return event, raw, nil
}

func (c *Client) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.in.Write(data)
	return err
}

type command struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

type Event struct {
	Type     string          `json:"type"`
	ID       string          `json:"id,omitempty"`
	Command  string          `json:"command,omitempty"`
	Success  bool            `json:"success,omitempty"`
	Error    string          `json:"error,omitempty"`
	Method   string          `json:"method,omitempty"`
	Messages json.RawMessage `json:"messages,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

func (e Event) CompactType() string {
	if e.Type == "" {
		return "pi.unknown"
	}
	return "pi." + e.Type
}

func (e Event) IsExtensionUIRequest() bool { return e.Type == "extension_ui_request" }

func (e Event) IsBlockingExtensionUI() bool {
	switch e.Method {
	case "select", "confirm", "input", "editor":
		return true
	default:
		return false
	}
}

func (e Event) IsFireAndForgetExtensionUI() bool {
	return e.IsExtensionUIRequest() && !e.IsBlockingExtensionUI()
}

func (e Event) SessionFile() string {
	if e.Type != "response" || e.Command != "get_state" || len(e.Data) == 0 {
		return ""
	}
	var payload struct {
		SessionFile string `json:"sessionFile"`
	}
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		return ""
	}
	return payload.SessionFile
}
