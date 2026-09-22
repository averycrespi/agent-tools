package httppolicy

import (
	"net/netip"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type AddressClass string

const (
	AddressPublic    AddressClass = "public"
	AddressPrivate   AddressClass = "private"
	AddressForbidden AddressClass = "forbidden"
)

// AddressFacts must describe the complete DNS answer and all Gateway-owned
// listener endpoints from the trusted proxy owner. No lookup occurs here.
// Every address is checked; forwarding must pin a checked IP, check each
// request even on pooled connections, and never resolve again after admission.
type AddressFacts struct {
	Complete         bool
	Addresses        []netip.Addr
	GatewayListeners []netip.AddrPort
}

func ClassifyAddress(ip netip.Addr) AddressClass {
	if !ip.IsValid() || ip.Zone() != "" {
		return AddressForbidden
	}
	ip = ip.Unmap()
	if ip == netip.MustParseAddr("fd00:ec2::254") || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return AddressForbidden
	}
	for _, p := range forbiddenPrefixes {
		if p.Contains(ip) {
			return AddressForbidden
		}
	}
	if ip.IsPrivate() || ip.IsLoopback() {
		return AddressPrivate
	}
	// Refuse transition/translation and unallocated IPv6 space instead of
	// guessing embedded address layouts or relying on the host's route table.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return AddressForbidden
	}
	if !ip.IsGlobalUnicast() {
		return AddressForbidden
	}
	return AddressPublic
}

var forbiddenPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func checkAddresses(d Destination, f AddressFacts, private bool) (contract.HTTPDecisionReason, error) {
	if !f.Complete || len(f.Addresses) == 0 || len(f.Addresses) > contract.HTTPAddressFacts || len(f.GatewayListeners) > contract.HTTPAddressFacts {
		return "", ErrInvalid
	}
	for _, l := range f.GatewayListeners {
		if !l.IsValid() || l.Port() == 0 || l.Addr().Zone() != "" {
			return "", ErrInvalid
		}
	}
	literal, literalErr := netip.ParseAddr(d.host)
	needsPrivate, forbidden := false, false
	for _, ip := range f.Addresses {
		if literalErr == nil && ip.Unmap() != literal {
			return "", ErrInvalid
		}
		switch ClassifyAddress(ip) {
		case AddressForbidden:
			forbidden = true
		case AddressPrivate:
			needsPrivate = true
		}
		for _, l := range f.GatewayListeners {
			if ip.Unmap() == l.Addr().Unmap() && d.port == l.Port() {
				forbidden = true
			}
		}
	}
	if forbidden {
		return contract.HTTPReasonAddressForbidden, nil
	}
	if needsPrivate && !private {
		return contract.HTTPReasonPrivateRequired, nil
	}
	return "", nil
}
