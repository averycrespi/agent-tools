package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPrivate(t *testing.T) {
	tests := []struct {
		name     string
		patterns string
		module   string
		want     bool
	}{
		{"exact match", "github.com/foo/bar", "github.com/foo/bar", true},
		{"wildcard match", "github.com/foo/*", "github.com/foo/bar", true},
		{"subpath of wildcard", "github.com/foo/*", "github.com/foo/bar/baz", true},
		{"no match", "github.com/foo/*", "github.com/other/repo", false},
		{"empty patterns", "", "github.com/any/thing", false},
		{"comma-separated", "github.com/a/*,github.com/b/*", "github.com/b/x", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := New(tc.patterns)
			assert.Equal(t, tc.want, r.IsPrivate(tc.module))
		})
	}
}
