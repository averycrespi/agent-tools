package main

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestStorageSizePreservesByteCompatibility(t *testing.T) {
	for _, input := range []string{"268435456", "256MiB", "262144KiB"} {
		flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
		var value int64
		storageSizeFlag(flags, &value, 0, "budget")
		require.NoError(t, flags.Set("traffic-budget-bytes", input))
		got, err := flags.GetInt64("traffic-budget-bytes")
		require.NoError(t, err)
		require.Equal(t, int64(256<<20), got)
	}
	for _, input := range []string{"-1", "1.5GiB", "999999999999GiB", "garbage", "1MiBextra"} {
		value := int64(7)
		err := (storageSizeValue{&value}).Set(input)
		require.Error(t, err)
		require.Equal(t, int64(7), value)
	}
}
