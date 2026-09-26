package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

// Keep the legacy int64 flag type and decimal String representation so installed
// service definitions and existing byte-form consumers retain their contract.
type storageSizeValue struct{ value *int64 }

func (v storageSizeValue) String() string { return strconv.FormatInt(*v.value, 10) }
func (v storageSizeValue) Type() string   { return "int64" }
func (v storageSizeValue) Set(input string) error {
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		bytes  int64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1000000000}, {"MB", 1000000}, {"KB", 1000}, {"B", 1},
	} {
		if strings.HasSuffix(input, unit.suffix) {
			multiplier = unit.bytes
			input = strings.TrimSuffix(input, unit.suffix)
			break
		}
	}
	n, err := strconv.ParseInt(input, 10, 64)
	if err != nil || n < 0 || n > (1<<63-1)/multiplier {
		return fmt.Errorf("use nonnegative bytes or an integer with KiB, MiB, GiB, KB, MB, GB or B")
	}
	*v.value = n * multiplier
	return nil
}

func storageSizeFlag(flags *pflag.FlagSet, value *int64, defaultBytes int64, usage string) {
	*value = defaultBytes
	flags.Var(storageSizeValue{value}, "traffic-budget-bytes", usage+" (bytes or sizes such as 256MiB)")
}
