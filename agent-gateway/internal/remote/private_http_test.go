package remote

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateHTTPAddressClasses(t *testing.T) {
	for _, address := range []string{"10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.0.1", "192.168.255.255", "fc00::1", "fdff:ffff::1", "::ffff:10.0.0.1"} {
		assert.True(t, privateHTTPAddress(netip.MustParseAddr(address)), address)
	}
	for _, address := range []string{"9.255.255.255", "11.0.0.0", "172.15.255.255", "172.32.0.0", "192.167.255.255", "192.169.0.0", "100.64.0.1", "169.254.169.254", "127.0.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "93.184.216.34", "192.0.2.1", "fe80::1", "ff02::1", "::", "::1", "2001:db8::1", "2001:4860:4860::8888", "::ffff:169.254.169.254", "fd00::1%eth0"} {
		assert.False(t, privateHTTPAddress(netip.MustParseAddr(address)), address)
	}
	assert.False(t, privateHTTPAddress(netip.Addr{}))
}

func TestPrivateHTTPPolicyIsEndpointAndSchemeScoped(t *testing.T) {
	policy := Policy{AllowLoopbackHTTP: true, AllowPrivateHTTP: true}
	for _, raw := range []string{"http://notes-mcp:8080/mcp", "http://notes-mcp.internal/mcp", "http://10.0.0.1/mcp", "http://[fd00::1]/mcp", "http://127.0.0.1/mcp"} {
		_, err := Parse(raw, policy)
		require.NoError(t, err, raw)
	}
	for _, raw := range []string{"http://93.184.216.34/mcp", "http://169.254.169.254/mcp", "http://100.64.0.1/mcp", "http://0.0.0.0/mcp", "http://[fe80::1]/mcp", "http://224.0.0.1/mcp", "http://bad_host/mcp"} {
		_, err := Parse(raw, policy)
		require.Error(t, err, raw)
	}
	_, err := Parse("http://notes-mcp:8080/mcp", Policy{AllowLoopbackHTTP: true})
	require.Error(t, err)
	https, err := Parse("https://notes-mcp/mcp", policy)
	require.NoError(t, err)
	factory := New(Options{Resolver: fixedResolver{[]netip.Addr{netip.MustParseAddr("10.0.0.1")}}, DialContext: func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("private HTTPS dialed")
		return nil, nil
	}})
	_, err = factory.dialPinned(t.Context(), "tcp", https)
	assert.ErrorIs(t, err, ErrAddressPolicy)
}

type changingPrivateResolver struct {
	answers []netip.Addr
	calls   int
}

func (r *changingPrivateResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	return r.answers, nil
}

func TestPrivateHTTPFreshResolutionPinsAndRejectsMixedRebinding(t *testing.T) {
	endpoint, err := Parse("http://notes-mcp:8080/mcp", Policy{AllowPrivateHTTP: true})
	require.NoError(t, err)
	resolver := &changingPrivateResolver{answers: []netip.Addr{netip.MustParseAddr("172.18.0.2")}}
	var targets []string
	stop := errors.New("fixture dial stop")
	factory := New(Options{Resolver: resolver, DialContext: func(_ context.Context, _, target string) (net.Conn, error) {
		targets = append(targets, target)
		return nil, stop
	}})
	_, err = factory.dialPinned(t.Context(), "tcp", endpoint)
	require.ErrorIs(t, err, stop)
	require.Equal(t, []string{"172.18.0.2:8080"}, targets)
	for _, forbidden := range []string{"93.184.216.34", "169.254.169.254", "127.0.0.1", "::1", "0.0.0.0", "ff02::1"} {
		resolver.answers = []netip.Addr{netip.MustParseAddr("172.18.0.2"), netip.MustParseAddr(forbidden)}
		_, err = factory.dialPinned(t.Context(), "tcp", endpoint)
		require.ErrorIs(t, err, ErrAddressPolicy)
	}
	resolver.answers = []netip.Addr{netip.MustParseAddr("169.254.169.254")}
	_, err = factory.dialPinned(t.Context(), "tcp", endpoint)
	require.ErrorIs(t, err, ErrAddressPolicy)
	assert.Len(t, targets, 1, "no fallback or replay dial")
	assert.Equal(t, 8, resolver.calls)
}
