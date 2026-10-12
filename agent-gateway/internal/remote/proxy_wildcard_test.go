package remote

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestProxyWildcardReservationRecheckedBeforeDial(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "10.0.0.2", "93.184.216.34"} {
		factory := New(Options{DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("self-target reached dial")
			return nil, nil
		}})
		destination, err := httppolicy.NewDestination(host, 8210)
		require.NoError(t, err)
		var listeners []netip.AddrPort
		pin, err := factory.ResolveProxy(t.Context(), destination, func() []netip.AddrPort { return listeners })
		require.NoError(t, err)
		listeners = []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:8210")}
		_, err = pin.Dial(t.Context(), true)
		require.ErrorIs(t, err, ErrAddressPolicy)
	}
}
