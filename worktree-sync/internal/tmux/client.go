package tmux

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	execclient "github.com/averycrespi/agent-tools/worktree-sync/internal/exec"
)

const separator = "|"

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
	return c.runSensitive(ctx, "", args...)
}

func (c *Client) runSensitive(ctx context.Context, sensitive string, args ...string) ([]byte, error) {
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
		diagnosticOutput := output
		if sensitive != "" {
			diagnosticOutput = []byte(strings.ReplaceAll(string(output), sensitive, "[redacted launch command]"))
		}
		diagnostic := execclient.Diagnostic(diagnosticOutput)
		if diagnostic != "" {
			return output, fmt.Errorf("tmux command failed: %w\n%s", err, diagnostic)
		}
		return output, fmt.Errorf("tmux command failed: %w", err)
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

func ids(output []byte) []string {
	text := strings.TrimSuffix(string(output), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func parseMetadataOptions(output string) (Metadata, error) {
	options := []string{"@wts-schema", "@wts-repository", "@wts-role", "@wts-identity"}
	found := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || !strings.HasPrefix(name, "@wts-") {
			continue
		}
		if strings.HasPrefix(value, "\"") {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return Metadata{}, fmt.Errorf("invalid quoted tmux metadata %s: %w", name, err)
			}
			value = decoded
		}
		found[name] = value
	}
	values := make([]string, len(options))
	for i, option := range options {
		values[i] = found[option]
	}
	return parseMetadata(values)
}

func (c *Client) metadata(ctx context.Context, target string, window bool) (Metadata, error) {
	args := []string{"show-options"}
	if window {
		args = append(args, "-w")
	}
	args = append(args, "-q", "-t", target)
	output, err := c.run(ctx, args...)
	if err != nil {
		return Metadata{}, err
	}
	return parseMetadataOptions(string(output))
}

func appendCommand(args []string, command ...string) []string {
	if len(args) > 0 {
		args = append(args, ";")
	}
	return append(args, command...)
}

func detailMarker(name string) string { return "__WTS_DETAIL_" + name + "__" }

func detailBlocks(output []byte, markers []string) (map[string]string, error) {
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	result := make(map[string]string, len(markers))
	position := 0
	for i, marker := range markers {
		if position >= len(lines) || lines[position] != marker {
			return nil, fmt.Errorf("tmux detail output missing marker %q", marker)
		}
		position++
		end := len(lines)
		if i+1 < len(markers) {
			for j := position; j < len(lines); j++ {
				if lines[j] == markers[i+1] {
					end = j
					break
				}
			}
			if end == len(lines) {
				return nil, fmt.Errorf("tmux detail output missing marker %q", markers[i+1])
			}
		}
		result[marker] = strings.Join(lines[position:end], "\n")
		position = end
	}
	return result, nil
}

func (c *Client) loadSessionDetails(ctx context.Context, session *Session) error {
	markers := []string{detailMarker("session-name"), detailMarker("session-metadata")}
	args := make([]string, 0)
	args = appendCommand(args, "display-message", "-p", "-F", markers[0])
	args = appendCommand(args, "display-message", "-p", "-t", session.ID, "-F", "#{session_name}")
	args = appendCommand(args, "display-message", "-p", "-F", markers[1])
	args = appendCommand(args, "show-options", "-q", "-t", session.ID)
	for i, window := range session.Windows {
		for _, field := range []string{"name", "path", "metadata"} {
			marker := detailMarker(fmt.Sprintf("window-%d-%s", i, field))
			markers = append(markers, marker)
			args = appendCommand(args, "display-message", "-p", "-F", marker)
			switch field {
			case "name":
				args = appendCommand(args, "display-message", "-p", "-t", window.ID, "-F", "#{window_name}")
			case "path":
				args = appendCommand(args, "display-message", "-p", "-t", window.ID, "-F", "#{pane_current_path}")
			case "metadata":
				args = appendCommand(args, "show-options", "-w", "-q", "-t", window.ID)
			}
		}
	}
	output, err := c.run(ctx, args...)
	if err != nil {
		return err
	}
	blocks, err := detailBlocks(output, markers)
	if err != nil {
		return err
	}
	session.Name = blocks[markers[0]]
	session.Metadata, err = parseMetadataOptions(blocks[markers[1]])
	if err != nil {
		return err
	}
	for i := range session.Windows {
		session.Windows[i].Name = blocks[detailMarker(fmt.Sprintf("window-%d-name", i))]
		session.Windows[i].Path = blocks[detailMarker(fmt.Sprintf("window-%d-path", i))]
		session.Windows[i].Metadata, err = parseMetadataOptions(blocks[detailMarker(fmt.Sprintf("window-%d-metadata", i))])
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	output, err := c.run(ctx, "list-sessions", "-F", "#{session_id}")
	if err != nil {
		if strings.Contains(string(output), "no server running") || strings.Contains(string(output), "failed to connect") || strings.Contains(string(output), "error connecting") {
			return Snapshot{Complete: true, Sessions: []Session{}}, nil
		}
		return Snapshot{Complete: false}, err
	}
	sessionIDs := ids(output)
	snapshot := Snapshot{Complete: true, Sessions: make([]Session, 0, len(sessionIDs))}
	for _, sessionID := range sessionIDs {
		windowsOut, windowsErr := c.run(ctx, "list-windows", "-t", sessionID, "-F", "#{window_id}")
		if windowsErr != nil {
			return Snapshot{Complete: false}, windowsErr
		}
		session := Session{ID: sessionID}
		for _, windowID := range ids(windowsOut) {
			session.Windows = append(session.Windows, Window{ID: windowID})
		}
		if detailErr := c.loadSessionDetails(ctx, &session); detailErr != nil {
			return Snapshot{Complete: false}, detailErr
		}
		snapshot.Sessions = append(snapshot.Sessions, session)
	}
	return snapshot, nil
}

func (c *Client) OwnsSession(ctx context.Context, id string, expected Metadata) (bool, error) {
	metadata, err := c.metadata(ctx, id, false)
	return err == nil && metadata == expected, err
}

func (c *Client) OwnsWindow(ctx context.Context, id string, expected Metadata) (bool, error) {
	metadata, err := c.metadata(ctx, id, true)
	return err == nil && metadata == expected, err
}

func metadataPairs(metadata Metadata) [][2]string {
	return [][2]string{{"@wts-schema", strconv.Itoa(metadata.Schema)}, {"@wts-repository", metadata.Repository}, {"@wts-role", metadata.Role}, {"@wts-identity", metadata.Identity}}
}

func (c *Client) setMetadata(ctx context.Context, target string, window bool, metadata Metadata) error {
	pairs := metadataPairs(metadata)
	args := make([]string, 0, len(pairs)*7)
	for i, pair := range pairs {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-option")
		if window {
			args = append(args, "-w")
		}
		args = append(args, "-t", target, pair[0], pair[1])
	}
	_, err := c.run(ctx, args...)
	return err
}

func (c *Client) CreateSession(ctx context.Context, name string, base Window) (string, error) {
	format := "#{session_id}" + separator + "#{window_id}"
	output, err := c.run(ctx, "new-session", "-d", "-P", "-F", format, "-s", name, "-n", base.Name, "-c", base.Path)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.TrimSpace(string(output)), separator)
	if len(parts) != 2 {
		return "", fmt.Errorf("tmux new-session returned malformed IDs")
	}
	sessionMetadata := base.Metadata
	sessionMetadata.Role = "session"
	sessionMetadata.Identity = base.Metadata.Repository
	if err := c.setMetadata(ctx, parts[0], false, sessionMetadata); err != nil {
		if _, cleanupErr := c.run(ctx, "kill-session", "-t", parts[0]); cleanupErr != nil {
			return parts[0], fmt.Errorf("tagging new session failed and cleanup failed; session %s may remain: %w", parts[0], errors.Join(err, cleanupErr))
		}
		return "", err
	}
	if err := c.setMetadata(ctx, parts[1], true, base.Metadata); err != nil {
		if _, cleanupErr := c.run(ctx, "kill-session", "-t", parts[0]); cleanupErr != nil {
			return parts[0], fmt.Errorf("tagging new base window failed and cleanup failed; session %s may remain: %w", parts[0], errors.Join(err, cleanupErr))
		}
		return "", err
	}
	return parts[0], nil
}

func (c *Client) RenameSession(ctx context.Context, id, name string) error {
	_, err := c.run(ctx, "rename-session", "-t", id, name)
	return err
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
		if _, cleanupErr := c.run(ctx, "kill-window", "-t", id); cleanupErr != nil {
			return id, fmt.Errorf("tagging new window failed and cleanup failed; window %s may remain: %w", id, errors.Join(err, cleanupErr))
		}
		return "", err
	}
	return id, nil
}

type RepairResult struct {
	Renamed         bool
	Respawned       bool
	MetadataOptions int
}

func (r RepairResult) Changed() bool {
	return r.Renamed || r.Respawned || r.MetadataOptions > 0
}

func (c *Client) ensureWindowOwnership(ctx context.Context, id string, expected Metadata) error {
	owned, err := c.OwnsWindow(ctx, id, expected)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("ownership changed; refusing window mutation")
	}
	return nil
}

func (c *Client) RepairWindow(ctx context.Context, id string, current, desired Window) (RepairResult, error) {
	result := RepairResult{}
	expected := current.Metadata
	if current.Name != desired.Name {
		if err := c.ensureWindowOwnership(ctx, id, expected); err != nil {
			return result, err
		}
		if _, err := c.run(ctx, "rename-window", "-t", id, desired.Name); err != nil {
			return result, err
		}
		result.Renamed = true
	}
	if current.Path != desired.Path {
		if err := c.ensureWindowOwnership(ctx, id, expected); err != nil {
			return result, err
		}
		if _, err := c.run(ctx, "respawn-window", "-k", "-t", id, "-c", desired.Path); err != nil {
			return result, err
		}
		result.Respawned = true
	}
	if current.Metadata != desired.Metadata {
		for _, pair := range metadataPairs(desired.Metadata) {
			if err := c.ensureWindowOwnership(ctx, id, expected); err != nil {
				return result, err
			}
			if _, err := c.run(ctx, "set-option", "-w", "-t", id, pair[0], pair[1]); err != nil {
				return result, err
			}
			switch pair[0] {
			case "@wts-schema":
				expected.Schema = desired.Metadata.Schema
			case "@wts-repository":
				expected.Repository = desired.Metadata.Repository
			case "@wts-role":
				expected.Role = desired.Metadata.Role
			case "@wts-identity":
				expected.Identity = desired.Metadata.Identity
			}
			result.MetadataOptions++
		}
	}
	return result, nil
}

func (c *Client) KillWindow(ctx context.Context, id string) error {
	_, err := c.run(ctx, "kill-window", "-t", id)
	return err
}
func (c *Client) KillSession(ctx context.Context, id string) error {
	_, err := c.run(ctx, "kill-session", "-t", id)
	return err
}

type LaunchError struct {
	TextSent  string
	EnterSent string
	Err       error
}

func (e *LaunchError) Error() string { return e.Err.Error() }
func (e *LaunchError) Unwrap() error { return e.Err }

func (c *Client) Launch(ctx context.Context, id string, expected Metadata, command string) error {
	owned, err := c.OwnsWindow(ctx, id, expected)
	if err != nil {
		return &LaunchError{TextSent: "no", EnterSent: "no", Err: err}
	}
	if !owned {
		return &LaunchError{TextSent: "no", EnterSent: "no", Err: fmt.Errorf("ownership changed; refusing launch")}
	}
	if _, err := c.runSensitive(ctx, command, "send-keys", "-t", id, "-l", "--", command); err != nil {
		return &LaunchError{TextSent: "unknown", EnterSent: "no", Err: err}
	}
	owned, err = c.OwnsWindow(ctx, id, expected)
	if err != nil {
		return &LaunchError{TextSent: "yes", EnterSent: "no", Err: err}
	}
	if !owned {
		return &LaunchError{TextSent: "yes", EnterSent: "no", Err: fmt.Errorf("ownership changed; refusing Enter delivery")}
	}
	if _, err := c.run(ctx, "send-keys", "-t", id, "Enter"); err != nil {
		return &LaunchError{TextSent: "yes", EnterSent: "unknown", Err: err}
	}
	return nil
}

func (c *Client) Attach(ctx context.Context, session string) error {
	args := []string{"-L", c.socket, "attach-session", "-t", session}
	return c.runner.Interactive(ctx, "", "tmux", args...)
}
