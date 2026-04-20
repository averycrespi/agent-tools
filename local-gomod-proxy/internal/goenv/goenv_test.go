package goenv

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRunner struct {
	out []byte
	err error
}

func (s stubRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return s.out, s.err
}

func TestRead_ParsesJSON(t *testing.T) {
	runner := stubRunner{out: []byte(`{"GOPRIVATE":"github.com/foo/*","GOMODCACHE":"/home/x/pkg/mod","GOVERSION":"go1.25.8"}`)}

	env, err := Read(context.Background(), runner)
	require.NoError(t, err)
	assert.Equal(t, "github.com/foo/*", env.GOPRIVATE)
	assert.Equal(t, "/home/x/pkg/mod", env.GOMODCACHE)
	assert.Equal(t, "go1.25.8", env.GOVERSION)
}

func TestRead_PropagatesError(t *testing.T) {
	runner := stubRunner{err: errors.New("boom")}
	_, err := Read(context.Background(), runner)
	assert.Error(t, err)
}
