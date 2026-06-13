package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddCommandDefinesReconfigureWithoutShorthand(t *testing.T) {
	flag := addCmd.Flags().Lookup("reconfigure")
	require.NotNil(t, flag)
	require.Empty(t, flag.Shorthand)
}
