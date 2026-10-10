package contract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProtocolActivityContract(t *testing.T) {
	require.Equal(t, 15*time.Minute, ProtocolActivityWindow("15m"))
	require.Equal(t, time.Hour, ProtocolActivityWindow("1h"))
	require.Equal(t, 24*time.Hour, ProtocolActivityWindow("24h"))
	require.Zero(t, ProtocolActivityWindow("1d"))
	from, until := "2026-10-10T20:00:00.000000000Z", "2026-10-10T21:00:00.000000000Z"
	require.True(t, ValidHistoryRange("", ""))
	require.True(t, ValidHistoryRange(from, until))
	for _, pair := range [][2]string{{from, ""}, {"", until}, {until, from}, {from, from}, {"2026-10-10T20:00:00Z", until}, {"2026-10-08T20:00:00.000000000Z", until}, {"2026-02-30T20:00:00.000000000Z", until}} {
		require.False(t, ValidHistoryRange(pair[0], pair[1]))
	}
}
