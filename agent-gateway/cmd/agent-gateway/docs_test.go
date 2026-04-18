package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNOTICEMentionsOnecli(t *testing.T) {
	data, err := os.ReadFile("../../NOTICE")
	require.NoError(t, err)
	assert.Contains(t, string(data), "onecli")
	assert.Contains(t, string(data), "no code is incorporated")
}

func TestREADMEHasRequiredSections(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	require.NoError(t, err)
	s := string(data)
	for _, h := range []string{"## Install", "## First run", "## Prior art"} {
		assert.Contains(t, s, h)
	}
}
