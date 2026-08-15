package git

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPush_VerifiesSinglePushURLAndConstructsRefspec(t *testing.T) {
	var calls []gitCall
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calls = append(calls, gitCall{dir: dir, name: name, args: append([]string(nil), args...)})
			switch len(calls) {
			case 1, 2:
				return nil, nil
			case 3:
				return []byte("git@github.com:acme/repo.git\n"), nil
			case 4:
				return []byte("pushed\n"), nil
			default:
				t.Fatalf("unexpected git call: %v", args)
				return nil, nil
			}
		},
	})

	out, err := c.Push(
		context.Background(),
		"/repo",
		"origin",
		"git@github.com:acme/repo.git",
		"refs/heads/topic",
		"refs/heads/main",
		false,
	)

	require.NoError(t, err)
	assert.Equal(t, "pushed", out)
	assert.Equal(t, []gitCall{
		{dir: "/repo", name: "git", args: []string{"check-ref-format", "refs/heads/topic"}},
		{dir: "/repo", name: "git", args: []string{"check-ref-format", "refs/heads/main"}},
		{dir: "/repo", name: "git", args: []string{"remote", "get-url", "--push", "--all", "--", "origin"}},
		{dir: "/repo", name: "git", args: []string{"push", "--", "origin", "refs/heads/topic:refs/heads/main"}},
	}, calls)
}

func TestPush_ForceUsesOnlyForceWithLease(t *testing.T) {
	var finalArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			switch {
			case len(args) > 0 && args[0] == "check-ref-format":
				return nil, nil
			case len(args) > 1 && args[0] == "remote":
				return []byte("git@github.com:acme/repo.git\n"), nil
			default:
				finalArgs = append([]string(nil), args...)
				return nil, nil
			}
		},
	})

	_, err := c.Push(context.Background(), "/repo", "origin", "git@github.com:acme/repo.git", "refs/tags/v1", "refs/tags/v1", true)

	require.NoError(t, err)
	assert.Equal(t, []string{"push", "--force-with-lease", "--", "origin", "refs/tags/v1:refs/tags/v1"}, finalArgs)
}

func TestPush_RejectsInvalidStructuredRefsBeforePush(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		destination string
	}{
		{name: "empty source deletion", source: "", destination: "refs/heads/main"},
		{name: "empty destination", source: "refs/heads/main", destination: ""},
		{name: "force prefix", source: "+refs/heads/main", destination: "refs/heads/main"},
		{name: "matching", source: ":", destination: "refs/heads/main"},
		{name: "wildcard", source: "refs/heads/*", destination: "refs/heads/main"},
		{name: "unsupported source namespace", source: "refs/remotes/origin/main", destination: "refs/heads/main"},
		{name: "unsupported destination namespace", source: "refs/heads/main", destination: "refs/notes/main"},
		{name: "malformed", source: "refs/heads/main..bad", destination: "refs/heads/main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pushed := false
			c := mustNewClient(t, &mockRunner{
				runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
					if len(args) > 0 && args[0] == "push" {
						pushed = true
					}
					if len(args) > 0 && args[0] == "check-ref-format" && tt.source == "refs/heads/main..bad" {
						return nil, fmt.Errorf("exit status 1")
					}
					return nil, nil
				},
			})

			_, err := c.Push(context.Background(), "/repo", "origin", "git@github.com:acme/repo.git", tt.source, tt.destination, false)

			require.Error(t, err)
			assert.False(t, pushed)
		})
	}
}

func TestPush_RejectsPushURLVerificationFailuresBeforePush(t *testing.T) {
	tests := []struct {
		name       string
		resolved   []byte
		resolveErr error
	}{
		{name: "zero URLs", resolved: nil},
		{name: "multiple URLs", resolved: []byte("ssh://one/repo.git\nssh://two/repo.git\n")},
		{name: "duplicate URLs", resolved: []byte("ssh://one/repo.git\nssh://one/repo.git\n")},
		{name: "mismatch", resolved: []byte("ssh://other/repo.git\n")},
		{name: "unexpected missing terminator", resolved: []byte("ssh://one/repo.git")},
		{name: "unknown remote", resolved: []byte("No such remote"), resolveErr: fmt.Errorf("exit status 2")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pushed := false
			c := mustNewClient(t, &mockRunner{
				runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
					switch {
					case len(args) > 0 && args[0] == "check-ref-format":
						return nil, nil
					case len(args) > 1 && args[0] == "remote":
						return tt.resolved, tt.resolveErr
					case len(args) > 0 && args[0] == "push":
						pushed = true
					}
					return nil, nil
				},
			})

			_, err := c.Push(context.Background(), "/repo", "origin", "ssh://one/repo.git", "refs/heads/main", "refs/heads/main", false)

			require.Error(t, err)
			assert.False(t, pushed)
		})
	}
}

func TestFetchAndPull_VerifyFetchURLAndUseNamedRemote(t *testing.T) {
	tests := []struct {
		name      string
		operation func(*Client) (string, error)
		finalArgs []string
	}{
		{
			name: "fetch preserves refspec",
			operation: func(c *Client) (string, error) {
				return c.Fetch(context.Background(), "/repo", "upstream", "ssh://fetch/repo.git", "refs/heads/main")
			},
			finalArgs: []string{"fetch", "--", "upstream", "refs/heads/main"},
		},
		{
			name: "pull preserves branch and rebase",
			operation: func(c *Client) (string, error) {
				return c.Pull(context.Background(), "/repo", "upstream", "ssh://fetch/repo.git", "main", true)
			},
			finalArgs: []string{"pull", "--rebase", "--", "upstream", "main"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []gitCall
			c := mustNewClient(t, &mockRunner{
				runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
					calls = append(calls, gitCall{dir: dir, name: name, args: append([]string(nil), args...)})
					if len(calls) == 1 {
						return []byte("ssh://fetch/repo.git\n"), nil
					}
					return []byte("ok\n"), nil
				},
			})

			out, err := tt.operation(c)

			require.NoError(t, err)
			assert.Equal(t, "ok", out)
			require.Len(t, calls, 2)
			assert.Equal(t, []string{"remote", "get-url", "--", "upstream"}, calls[0].args)
			assert.Equal(t, tt.finalArgs, calls[1].args)
		})
	}
}

func TestFetchAndPull_RejectMismatchBeforeOperation(t *testing.T) {
	tests := []struct {
		name      string
		operation func(*Client) error
	}{
		{
			name: "fetch",
			operation: func(c *Client) error {
				_, err := c.Fetch(context.Background(), "/repo", "origin", "ssh://expected/repo.git", "")
				return err
			},
		},
		{
			name: "pull",
			operation: func(c *Client) error {
				_, err := c.Pull(context.Background(), "/repo", "origin", "ssh://expected/repo.git", "", false)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := mustNewClient(t, &mockRunner{
				runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
					calls++
					return []byte("ssh://different/repo.git\n"), nil
				},
			})

			err := tt.operation(c)

			require.Error(t, err)
			assert.Equal(t, 1, calls)
		})
	}
}

func TestRemoteOperations_RejectHTTPUserinfoWithoutLeakingURL(t *testing.T) {
	secretURLs := []string{
		"https://alice@example.com/acme/repo.git",
		"HTTP://alice:super-secret@example.com/acme/repo.git",
	}
	operations := []struct {
		name string
		run  func(*Client, string) error
	}{
		{
			name: "push supplied URL",
			run: func(c *Client, remoteURL string) error {
				_, err := c.Push(context.Background(), "/repo", "origin", remoteURL, "refs/heads/main", "refs/heads/main", false)
				return err
			},
		},
		{
			name: "fetch supplied URL",
			run: func(c *Client, remoteURL string) error {
				_, err := c.Fetch(context.Background(), "/repo", "origin", remoteURL, "")
				return err
			},
		},
		{
			name: "pull supplied URL",
			run: func(c *Client, remoteURL string) error {
				_, err := c.Pull(context.Background(), "/repo", "origin", remoteURL, "", false)
				return err
			},
		},
	}

	for _, op := range operations {
		for _, secretURL := range secretURLs {
			t.Run(op.name+" "+secretURL, func(t *testing.T) {
				calledGit := false
				c := mustNewClient(t, &mockRunner{
					runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
						calledGit = true
						return nil, nil
					},
				})

				err := op.run(c, secretURL)

				require.Error(t, err)
				assert.NotContains(t, err.Error(), secretURL)
				assert.NotContains(t, err.Error(), "super-secret")
				assert.False(t, calledGit)
			})
		}
	}
}

func TestRemoteOperations_RejectResolvedHTTPUserinfoWithoutLeakingURL(t *testing.T) {
	secretURL := "https://alice:super-secret@example.com/acme/repo.git"
	operations := []struct {
		name string
		run  func(*Client) error
	}{
		{
			name: "push",
			run: func(c *Client) error {
				_, err := c.Push(context.Background(), "/repo", "origin", "https://example.com/acme/repo.git", "refs/heads/main", "refs/heads/main", false)
				return err
			},
		},
		{
			name: "fetch",
			run: func(c *Client) error {
				_, err := c.Fetch(context.Background(), "/repo", "origin", "https://example.com/acme/repo.git", "")
				return err
			},
		},
		{
			name: "pull",
			run: func(c *Client) error {
				_, err := c.Pull(context.Background(), "/repo", "origin", "https://example.com/acme/repo.git", "", false)
				return err
			},
		},
	}

	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			operationRan := false
			c := mustNewClient(t, &mockRunner{
				runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
					switch args[0] {
					case "check-ref-format":
						return nil, nil
					case "remote":
						return []byte(secretURL + "\n"), nil
					default:
						operationRan = true
						return nil, nil
					}
				},
			})

			err := op.run(c)

			require.Error(t, err)
			assert.NotContains(t, err.Error(), secretURL)
			assert.NotContains(t, err.Error(), "super-secret")
			assert.False(t, operationRan)
		})
	}
}

func TestRemoteOperations_RejectCredentialURLAsRemoteWithoutLeakingIt(t *testing.T) {
	secretURL := "https://alice:super-secret@example.com/acme/repo.git"
	calledGit := false
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calledGit = true
			return nil, nil
		},
	})

	_, err := c.Fetch(context.Background(), "/repo", secretURL, "https://example.com/acme/repo.git", "")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretURL)
	assert.NotContains(t, err.Error(), "super-secret")
	assert.False(t, calledGit)
}

func TestRemoteVerification_PreservesLookupContextErrors(t *testing.T) {
	c, err := NewClientWithTimeout(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}, nil, true, time.Millisecond)
	require.NoError(t, err)

	_, err = c.Fetch(context.Background(), "/repo", "origin", "ssh://example.com/repo.git", "")

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRemoteOperations_AcceptSCPStyleSSHUsername(t *testing.T) {
	calls := 0
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("git@github.com:acme/repo.git\n"), nil
			}
			return nil, nil
		},
	})

	_, err := c.Fetch(context.Background(), "/repo", "origin", "git@github.com:acme/repo.git", "")

	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}
