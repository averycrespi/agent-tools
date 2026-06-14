package main

import (
	"context"
	"strings"
	"testing"
)

func TestValidateArtifactParentChecksSandboxDirectoryIsWritable(t *testing.T) {
	client := &recordingSandboxClient{}
	newSandboxClient = func() (sandboxClient, error) { return client, nil }
	t.Cleanup(func() { newSandboxClient = defaultNewSandboxClient })

	if err := defaultValidateArtifactParent(t.TempDir()); err != nil {
		t.Fatalf("defaultValidateArtifactParent() error = %v", err)
	}
	if len(client.commands) != 1 {
		t.Fatalf("commands = %#v, want one sandbox validation command", client.commands)
	}
	command := strings.Join(client.commands[0], " ")
	if !strings.Contains(command, "-d") || !strings.Contains(command, "-w") {
		t.Fatalf("sandbox command = %q, want directory and writable checks", command)
	}
}

type recordingSandboxClient struct {
	commands [][]string
}

func (r *recordingSandboxClient) Exec(_ context.Context, workdir string, args ...string) ([]byte, error) {
	r.commands = append(r.commands, append([]string{workdir}, args...))
	return nil, nil
}
