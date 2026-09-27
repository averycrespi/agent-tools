package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyHTTPLaunchContext(t *testing.T) {
	for _, test := range []struct {
		platform, label string
		disabled        bool
	}{
		{"darwin", Label, true},
		{"darwin", "", false},
		{"darwin", "0", false},
		{"darwin", Label + ".other", false},
		{"linux", Label, false},
	} {
		require.Equal(t, test.disabled, legacyHTTPDisabled(test.platform, test.label))
	}
}
