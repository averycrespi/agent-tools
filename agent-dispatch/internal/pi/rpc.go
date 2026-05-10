package pi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

type Client struct {
	in  io.WriteCloser
	out *bufio.Scanner
}

func NewClient(stdin io.WriteCloser, stdout io.Reader) *Client {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	return &Client{in: stdin, out: scanner}
}

func (c *Client) Prompt(text string) error { return c.send(command{Type: "prompt", Prompt: text}) }
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
	if !c.out.Scan() {
		if err := c.out.Err(); err != nil {
			return Event{}, nil, err
		}
		return Event{}, nil, io.EOF
	}
	raw := append([]byte(nil), c.out.Bytes()...)
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
	Prompt  string `json:"prompt,omitempty"`
	Message string `json:"message,omitempty"`
}

type Event struct {
	Type     string          `json:"type"`
	ID       string          `json:"id,omitempty"`
	Method   string          `json:"method,omitempty"`
	Messages json.RawMessage `json:"messages,omitempty"`
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
