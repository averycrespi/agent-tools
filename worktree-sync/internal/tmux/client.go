package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const separator = "\x1f"

type runner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
	Interactive(context.Context, string, string, ...string) error
}

type Client struct {
	runner  runner
	socket  string
	timeout time.Duration
}

func New(runner runner, socket string, timeout time.Duration) *Client {
	if socket == "" {
		socket = SocketName
	}
	return &Client{runner: runner, socket: socket, timeout: timeout}
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	full := append([]string{"-L", c.socket}, args...)
	output, err := c.runner.Run(ctx, "", "tmux", full...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return output, fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func parseMetadata(fields []string) (Metadata, error) {
	metadata := Metadata{Repository: fields[1], Role: fields[2], Identity: fields[3]}
	if fields[0] == "" {
		return metadata, nil
	}
	schema, err := strconv.Atoi(fields[0])
	if err != nil {
		return Metadata{}, fmt.Errorf("invalid metadata schema %q", fields[0])
	}
	metadata.Schema = schema
	return metadata, nil
}

func records(output []byte, fields int) ([][]string, error) {
	text := strings.TrimSuffix(string(output), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	result := make([][]string, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, separator)
		if len(parts) != fields {
			return nil, fmt.Errorf("tmux snapshot record has %d fields, want %d", len(parts), fields)
		}
		result = append(result, parts)
	}
	return result, nil
}

func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	sessionFormat := strings.Join([]string{"#{session_id}", "#{session_name}", "#{@wts-schema}", "#{@wts-repository}", "#{@wts-role}", "#{@wts-identity}"}, separator)
	output, err := c.run(ctx, "list-sessions", "-F", sessionFormat)
	if err != nil {
		if strings.Contains(string(output), "no server running") || strings.Contains(string(output), "failed to connect") {
			return Snapshot{Complete: true, Sessions: []Session{}}, nil
		}
		return Snapshot{Complete: false}, err
	}
	sessionRecords, err := records(output, 6)
	if err != nil {
		return Snapshot{Complete: false}, err
	}
	snapshot := Snapshot{Complete: true, Sessions: make([]Session, 0, len(sessionRecords))}
	windowFormat := strings.Join([]string{"#{session_id}", "#{window_id}", "#{window_name}", "#{pane_current_path}", "#{@wts-schema}", "#{@wts-repository}", "#{@wts-role}", "#{@wts-identity}"}, separator)
	for _, record := range sessionRecords {
		metadata, metadataErr := parseMetadata(record[2:])
		if metadataErr != nil {
			return Snapshot{Complete: false}, metadataErr
		}
		session := Session{ID: record[0], Name: record[1], Metadata: metadata}
		windowsOut, windowsErr := c.run(ctx, "list-windows", "-t", session.ID, "-F", windowFormat)
		if windowsErr != nil {
			return Snapshot{Complete: false}, windowsErr
		}
		windowRecords, parseErr := records(windowsOut, 8)
		if parseErr != nil {
			return Snapshot{Complete: false}, parseErr
		}
		for _, windowRecord := range windowRecords {
			windowMetadata, windowMetadataErr := parseMetadata(windowRecord[4:])
			if windowMetadataErr != nil {
				return Snapshot{Complete: false}, windowMetadataErr
			}
			session.Windows = append(session.Windows, Window{ID: windowRecord[1], Name: windowRecord[2], Path: windowRecord[3], Metadata: windowMetadata})
		}
		snapshot.Sessions = append(snapshot.Sessions, session)
	}
	return snapshot, nil
}

func (c *Client) setMetadata(ctx context.Context, target string, window bool, metadata Metadata) error {
	pairs := [][2]string{{"@wts-schema", strconv.Itoa(metadata.Schema)}, {"@wts-repository", metadata.Repository}, {"@wts-role", metadata.Role}, {"@wts-identity", metadata.Identity}}
	for _, pair := range pairs {
		args := []string{"set-option"}
		if window {
			args = append(args, "-w")
		}
		args = append(args, "-t", target, pair[0], pair[1])
		if _, err := c.run(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) CreateSession(ctx context.Context, name string, base Window) error {
	format := "#{session_id}" + separator + "#{window_id}"
	output, err := c.run(ctx, "new-session", "-d", "-P", "-F", format, "-s", name, "-n", base.Name, "-c", base.Path)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimSpace(string(output)), separator)
	if len(parts) != 2 {
		return fmt.Errorf("tmux new-session returned malformed IDs")
	}
	sessionMetadata := base.Metadata
	sessionMetadata.Role = "session"
	sessionMetadata.Identity = base.Metadata.Repository
	if err := c.setMetadata(ctx, parts[0], false, sessionMetadata); err != nil {
		return err
	}
	return c.setMetadata(ctx, parts[1], true, base.Metadata)
}

func (c *Client) CreateWindow(ctx context.Context, sessionID string, window Window) (string, error) {
	output, err := c.run(ctx, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", sessionID, "-n", window.Name, "-c", window.Path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(output))
	if id == "" {
		return "", fmt.Errorf("tmux new-window returned no ID")
	}
	if err := c.setMetadata(ctx, id, true, window.Metadata); err != nil {
		return id, err
	}
	return id, nil
}

func (c *Client) RepairWindow(ctx context.Context, id string, window Window) error {
	if _, err := c.run(ctx, "rename-window", "-t", id, window.Name); err != nil {
		return err
	}
	if _, err := c.run(ctx, "respawn-window", "-k", "-t", id, "-c", window.Path); err != nil {
		return err
	}
	return c.setMetadata(ctx, id, true, window.Metadata)
}

func (c *Client) KillWindow(ctx context.Context, id string) error {
	_, err := c.run(ctx, "kill-window", "-t", id)
	return err
}
func (c *Client) KillSession(ctx context.Context, id string) error {
	_, err := c.run(ctx, "kill-session", "-t", id)
	return err
}

func (c *Client) Launch(ctx context.Context, id, command string) error {
	if _, err := c.run(ctx, "send-keys", "-t", id, "-l", "--", command); err != nil {
		return err
	}
	_, err := c.run(ctx, "send-keys", "-t", id, "Enter")
	return err
}

func (c *Client) Attach(ctx context.Context, session string) error {
	args := []string{"-L", c.socket, "attach-session", "-t", session}
	return c.runner.Interactive(ctx, "", "tmux", args...)
}
