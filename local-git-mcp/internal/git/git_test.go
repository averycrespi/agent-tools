package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner is a test double for exec.Runner.
type mockRunner struct {
	runDirFunc func(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return m.RunDir(ctx, "", name, args...)
}

func (m *mockRunner) RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if m.runDirFunc != nil {
		return m.runDirFunc(ctx, dir, name, args...)
	}
	return nil, nil
}

func mustNewClient(t *testing.T, runner *mockRunner) *Client {
	t.Helper()
	client, err := NewClient(runner, nil, true)
	require.NoError(t, err)
	return client
}

func TestCloneGitHubRepo_Success(t *testing.T) {
	destinationDir := t.TempDir()
	targetPath := filepath.Join(destinationDir, "repo")
	type call struct {
		dir  string
		args []string
	}
	var calls []call
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			require.Equal(t, "git", name)
			calls = append(calls, call{dir: dir, args: args})
			if len(calls) == 2 {
				return []byte(".git\n"), nil
			}
			return []byte("cloned\n"), nil
		},
	}, []string{destinationDir}, false)
	require.NoError(t, err)

	repoPath, err := c.CloneGitHubRepo(context.Background(), "owner/repo", destinationDir)

	require.NoError(t, err)
	assert.Equal(t, targetPath, repoPath)
	require.Len(t, calls, 2)
	assert.Equal(t, destinationDir, calls[0].dir)
	assert.Equal(t, []string{"clone", "--", "git@github.com:owner/repo.git", targetPath}, calls[0].args)
	assert.Equal(t, targetPath, calls[1].dir)
	assert.Equal(t, []string{"rev-parse", "--git-dir"}, calls[1].args)
}

func TestCloneGitHubRepo_RejectsMalformedRepositories(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			t.Fatal("git should not run for malformed repositories")
			return nil, nil
		},
	})
	for _, repository := range []string{
		"",
		"owner",
		"owner/",
		"/repo",
		"owner/repo/extra",
		"../repo",
		"owner/..",
		"owner/re po",
		"https://github.com/owner/repo",
		"git@github.com:owner/repo.git",
		"owner/repo.git?ref=main",
		"owner/🔥",
	} {
		t.Run(repository, func(t *testing.T) {
			_, err := c.CloneGitHubRepo(context.Background(), repository, "/tmp")
			require.Error(t, err)
			if repository == "" {
				assert.ErrorContains(t, err, "repository is required")
			} else {
				assert.ErrorContains(t, err, "repository must be in owner/repo form")
			}
		})
	}
}

func TestCloneGitHubRepo_RejectsRelativeDestinationDir(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			t.Fatal("git should not run for invalid destination_dir")
			return nil, nil
		},
	})

	_, err := c.CloneGitHubRepo(context.Background(), "owner/repo", "relative/path")

	assert.ErrorContains(t, err, "destination_dir must be an absolute path")
}

func TestCloneGitHubRepo_RejectsDestinationOutsideAllowedPaths(t *testing.T) {
	allowedDir := t.TempDir()
	destinationDir := t.TempDir()
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			t.Fatal("git should not run for destination_dir outside allowed paths")
			return nil, nil
		},
	}, []string{allowedDir}, false)
	require.NoError(t, err)

	_, err = c.CloneGitHubRepo(context.Background(), "owner/repo", destinationDir)

	assert.ErrorContains(t, err, "outside allowed paths")
}

func TestCloneGitHubRepo_RejectsMissingDestinationDir(t *testing.T) {
	destinationDir := filepath.Join(t.TempDir(), "missing")
	c := mustNewClient(t, &mockRunner{})

	_, err := c.CloneGitHubRepo(context.Background(), "owner/repo", destinationDir)

	assert.ErrorContains(t, err, "destination_dir must be an existing directory")
}

func TestCloneGitHubRepo_RejectsDestinationFile(t *testing.T) {
	destinationFile := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(destinationFile, []byte("not a dir"), 0o600))
	c := mustNewClient(t, &mockRunner{})

	_, err := c.CloneGitHubRepo(context.Background(), "owner/repo", destinationFile)

	assert.ErrorContains(t, err, "destination_dir must be a directory")
}

func TestCloneGitHubRepo_RejectsSymlinkDestinationDir(t *testing.T) {
	realDir := t.TempDir()
	symlinkDir := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(realDir, symlinkDir))
	c := mustNewClient(t, &mockRunner{})

	_, err := c.CloneGitHubRepo(context.Background(), "owner/repo", symlinkDir)

	assert.ErrorContains(t, err, "destination_dir must not be a symlink")
}

func TestCloneGitHubRepo_RejectsExistingTargetPath(t *testing.T) {
	destinationDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(destinationDir, "repo"), 0o700))
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			t.Fatal("git should not run when target exists")
			return nil, nil
		},
	})

	_, err := c.CloneGitHubRepo(context.Background(), "owner/repo", destinationDir)

	assert.ErrorContains(t, err, "target directory already exists")
}

func TestCloneGitHubRepo_Error(t *testing.T) {
	destinationDir := t.TempDir()
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: could not read from remote repository"), fmt.Errorf("exit status 128")
		},
	})

	_, err := c.CloneGitHubRepo(context.Background(), "owner/repo", destinationDir)

	assert.ErrorContains(t, err, "git clone failed")
	assert.ErrorContains(t, err, "could not read from remote repository")
}

func TestCloneGitHubRepo_PostCloneValidationError(t *testing.T) {
	destinationDir := t.TempDir()
	calls := 0
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("cloned\n"), nil
			}
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})

	_, err := c.CloneGitHubRepo(context.Background(), "owner/repo", destinationDir)

	assert.ErrorContains(t, err, "validating cloned repository")
	assert.ErrorContains(t, err, "not a git repository")
}

func TestCloneGitHubRepo_TimesOutBlockedCommand(t *testing.T) {
	destinationDir := t.TempDir()
	c, err := NewClientWithTimeout(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}, nil, true, time.Millisecond)
	require.NoError(t, err)

	_, err = c.CloneGitHubRepo(context.Background(), "owner/repo", destinationDir)

	assert.ErrorContains(t, err, "git clone failed")
	assert.ErrorContains(t, err, context.DeadlineExceeded.Error())
}

func TestPush_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Everything up-to-date\n"), nil
		},
	})
	out, err := c.Push(context.Background(), "/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Everything up-to-date", out)
	assert.Equal(t, []string{"push", "--", "origin"}, capturedArgs)
}

func TestPush_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push(context.Background(), "/repo", "origin", "refs/heads/main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "--", "origin", "refs/heads/main"}, capturedArgs)
}

func TestPush_ForceWithLease(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push(context.Background(), "/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "--force-with-lease", "--", "origin"}, capturedArgs)
}

func TestPush_Error(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("error: failed to push"), fmt.Errorf("exit status 1")
		},
	})
	_, err := c.Push(context.Background(), "/repo", "origin", "", false)
	assert.ErrorContains(t, err, "git push failed")
}

func TestPull_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Already up to date.\n"), nil
		},
	})
	out, err := c.Pull(context.Background(), "/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Already up to date.", out)
	assert.Equal(t, []string{"pull", "--", "origin"}, capturedArgs)
}

func TestPull_WithBranch(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull(context.Background(), "/repo", "origin", "main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "--", "origin", "main"}, capturedArgs)
}

func TestPull_WithRebase(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull(context.Background(), "/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "--rebase", "--", "origin"}, capturedArgs)
}

func TestFetch_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch(context.Background(), "/repo", "origin", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "--", "origin"}, capturedArgs)
}

func TestFetch_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch(context.Background(), "/repo", "origin", "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "--", "origin", "refs/heads/main"}, capturedArgs)
}

func TestFetch_TimesOutBlockedCommand(t *testing.T) {
	c, err := NewClientWithTimeout(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}, nil, true, time.Millisecond)
	require.NoError(t, err)

	_, err = c.Fetch(context.Background(), "/repo", "origin", "")

	assert.ErrorContains(t, err, "git fetch failed")
	assert.ErrorContains(t, err, context.DeadlineExceeded.Error())
}

func TestListRemoteRefs_Success(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("abc123\trefs/heads/main\ndef456\trefs/heads/feature\n"), nil
		},
	})
	refs, err := c.ListRemoteRefs(context.Background(), "/repo", "origin")
	require.NoError(t, err)
	assert.Equal(t, []Ref{
		{SHA: "abc123", Ref: "refs/heads/main"},
		{SHA: "def456", Ref: "refs/heads/feature"},
	}, refs)
}

func TestListRemoteRefs_Error(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ListRemoteRefs(context.Background(), "/repo", "origin")
	assert.ErrorContains(t, err, "git ls-remote failed")
}

func TestListRemotes_Success(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("origin\tgit@github.com:user/repo.git (fetch)\norigin\tgit@github.com:user/repo.git (push)\n"), nil
		},
	})
	remotes, err := c.ListRemotes(context.Background(), "/repo")
	require.NoError(t, err)
	assert.Equal(t, []Remote{
		{Name: "origin", FetchURL: "git@github.com:user/repo.git", PushURL: "git@github.com:user/repo.git"},
	}, remotes)
}

func TestListRemotes_Error(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ListRemotes(context.Background(), "/repo")
	assert.ErrorContains(t, err, "git remote failed")
}

func TestValidateRepo_RelativePath(t *testing.T) {
	c := mustNewClient(t, &mockRunner{})
	_, err := c.ValidateRepo(context.Background(), "relative/path")
	assert.ErrorContains(t, err, "must be an absolute path")
}

func TestValidateRepo_NotAGitRepo(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ValidateRepo(context.Background(), "/some/path")
	assert.ErrorContains(t, err, "not a git repository")
}

func TestValidateRepo_AllowedExactPath(t *testing.T) {
	var capturedDir string
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedDir = dir
			return []byte(".git\n"), nil
		},
	}, []string{"/some/repo"}, false)
	require.NoError(t, err)
	validatedPath, err := c.ValidateRepo(context.Background(), "/some/repo")
	require.NoError(t, err)
	assert.Equal(t, "/some/repo", validatedPath)
	assert.Equal(t, "/some/repo", capturedDir)
}

func TestValidateRepo_ReturnsCleanedPath(t *testing.T) {
	var capturedDir string
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedDir = dir
			return []byte(".git\n"), nil
		},
	}, []string{"/some/repo"}, false)
	require.NoError(t, err)
	validatedPath, err := c.ValidateRepo(context.Background(), "/some/repo/../repo")
	require.NoError(t, err)
	assert.Equal(t, "/some/repo", validatedPath)
	assert.Equal(t, "/some/repo", capturedDir)
}

func TestValidateRepo_AllowedDescendantPath(t *testing.T) {
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	}, []string{"/some"}, false)
	require.NoError(t, err)
	_, err = c.ValidateRepo(context.Background(), "/some/repo")
	require.NoError(t, err)
}

func TestValidateRepo_RejectsSiblingPrefix(t *testing.T) {
	calledGit := false
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calledGit = true
			return nil, nil
		},
	}, []string{"/repo"}, false)
	require.NoError(t, err)
	_, err = c.ValidateRepo(context.Background(), "/repo2")
	assert.ErrorContains(t, err, "outside allowed paths")
	assert.ErrorContains(t, err, "/repo")
	assert.False(t, calledGit)
}

func TestValidateRepo_Valid(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	})
	_, err := c.ValidateRepo(context.Background(), "/some/repo")
	require.NoError(t, err)
}

func TestNewClient_RequiresAllowedPaths(t *testing.T) {
	_, err := NewClient(&mockRunner{}, nil, false)
	assert.ErrorContains(t, err, "at least one allowed path is required")
}

func TestNewClient_RejectsRelativeAllowedPath(t *testing.T) {
	_, err := NewClient(&mockRunner{}, []string{"relative/path"}, false)
	assert.ErrorContains(t, err, "allowed path must be absolute")
}

func TestNewClient_RejectsAllowAllWithExplicitPaths(t *testing.T) {
	_, err := NewClient(&mockRunner{}, []string{"/repo"}, true)
	assert.ErrorContains(t, err, "cannot be combined")
}

func TestNewClient_AllowAllPaths(t *testing.T) {
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	}, nil, true)
	require.NoError(t, err)
	_, err = c.ValidateRepo(context.Background(), "/any/repo")
	require.NoError(t, err)
}
