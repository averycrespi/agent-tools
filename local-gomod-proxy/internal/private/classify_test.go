package private

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name       string
		msg        string
		wantNotFnd bool
	}{
		{"unknown revision", "github.com/foo/bar@v99.99.99: invalid version: unknown revision v99.99.99", true},
		{"invalid version", "github.com/foo/bar@not-a-version: invalid version: syntax error", true},
		{"repository does not exist", "reading github.com/foo/ghost: repository does not exist", true},
		{"repository not found", "cloning https://github.com/foo/ghost: repository not found", true},
		{"no matching versions", "no matching versions for query \"latest\"", true},
		{"upstream 404", "reading https://proxy.golang.org/...: 404 Not Found", true},
		{"upstream 410", "reading https://proxy.golang.org/...: 410 Gone", true},

		{"auth failure stays as-is", "could not read Username for 'https://github.com': terminal prompts disabled", false},
		{"permission denied stays as-is", "Permission denied (publickey)", false},
		{"rate limit stays as-is", "403 response from api.github.com", false},
		{"network error stays as-is", "dial tcp: i/o timeout", false},
		{"generic failure stays as-is", "boom", false},
		{"empty stays as-is", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := errors.New("base")
			got := classifyError(base, tc.msg)
			if tc.wantNotFnd {
				assert.ErrorIs(t, got, ErrModuleNotFound)
				assert.Contains(t, got.Error(), tc.msg)
			} else {
				assert.NotErrorIs(t, got, ErrModuleNotFound)
				assert.Same(t, base, got)
			}
		})
	}
}
