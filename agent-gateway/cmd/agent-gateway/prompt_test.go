package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfirm_Force(t *testing.T) {
	var out bytes.Buffer
	ok, err := confirm(strings.NewReader(""), &out, false, true, "Proceed?")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Empty(t, out.String(), "force should not print any prompt")
}

func TestConfirm_NonTTY_Refuses(t *testing.T) {
	var out bytes.Buffer
	_, err := confirm(strings.NewReader(""), &out, false, false, "Proceed?")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
}

func TestConfirm_TTY_Yes(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n", "yes\n", "YES\n"} {
		var out bytes.Buffer
		ok, err := confirm(strings.NewReader(answer), &out, true, false, "Proceed?")
		require.NoError(t, err)
		assert.True(t, ok, "answer %q should confirm", answer)
		assert.Contains(t, out.String(), "Proceed?")
	}
}

func TestConfirm_TTY_No(t *testing.T) {
	for _, answer := range []string{"n\n", "N\n", "no\n", "\n", "anything\n"} {
		var out bytes.Buffer
		ok, err := confirm(strings.NewReader(answer), &out, true, false, "Proceed?")
		require.NoError(t, err)
		assert.False(t, ok, "answer %q should cancel", answer)
		assert.Contains(t, out.String(), "cancelled")
	}
}
