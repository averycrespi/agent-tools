package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6InvocationAPI(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("root.go")
	require.NoError(t, err)
	source := string(contents)
	assert.Equal(t, 1, strings.Count(source, "runtime.ControlAPI()"))
	assert.Equal(t, 1, strings.Count(source, "Invocations:   controlAPI.Invocations"))
	assert.NotContains(t, source, "/internal/invocation")
	assert.NotContains(t, source, "invocation.NewRepository(")
}
