package httppolicy

import (
	"net/netip"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestWildcardListenerExclusion(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "10.0.0.2", "93.184.216.34"} {
		destination, err := NewDestination(host, 8210)
		require.NoError(t, err)
		facts := AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr(host)}, GatewayListeners: []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:8210")}}
		reason, err := checkAddresses(destination, facts, true)
		require.NoError(t, err)
		require.Equal(t, contract.HTTPReasonAddressForbidden, reason)
		destination, err = NewDestination(host, 8443)
		require.NoError(t, err)
		reason, err = checkAddresses(destination, facts, true)
		require.NoError(t, err)
		require.Empty(t, reason)
	}
}
