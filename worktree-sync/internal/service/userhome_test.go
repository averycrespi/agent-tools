package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserHomeRejectsRelativePath(t *testing.T) {
	t.Setenv("HOME", "relative-home")
	_, err := userHome()
	require.ErrorContains(t, err, "absolute")
}
