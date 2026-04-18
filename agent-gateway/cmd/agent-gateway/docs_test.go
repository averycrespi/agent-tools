package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestREADMEHasRequiredSections(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	require.NoError(t, err)
	s := string(data)
	for _, h := range []string{"## Install", "## First run", "## Prior art"} {
		assert.Contains(t, s, h)
	}
}

func TestDesignHasRequiredSections(t *testing.T) {
	data, err := os.ReadFile("../../DESIGN.md")
	require.NoError(t, err)
	s := string(data)
	for _, h := range []string{"## 1. Summary", "## 3. Architecture", "## 10. Prior Art"} {
		assert.Contains(t, s, h)
	}
}
