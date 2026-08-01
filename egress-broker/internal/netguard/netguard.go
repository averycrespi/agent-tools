// Package netguard guards upstream dials against SSRF and cloud-metadata
// abuse.
//
// Ported from the agent-gateway branch of this repository
// (origin/agent-gateway, internal/netguard), with three changes:
//
//   - The allowPrivate escape hatch is gone. The source let configuration
//     disable every check except the IMDS one; nothing in this tool's design
//     needs that, and a knob that turns off SSRF protection is a knob that
//     eventually gets turned on.
//   - Hosts written in an alternate IP encoding (decimal, hex, octal, or a
//     short dotted form such as "127.1") are rejected outright rather than
//     left to fail DNS resolution by accident. Relying on the resolver to
//     reject them makes the guarantee depend on resolver behaviour.
//   - Addresses go through net/netip and are unmapped, so ::ffff:127.0.0.1
//     cannot be used as a second spelling of a blocked address.
//
// The package exists because of a real hole in the source it is ported from:
// that branch guarded only the MITM transport, while its tunnel path called a
// bare net.Dial (internal/proxy/connect.go:237) and routed every IP literal to
// the tunnel — so an agent could reach 169.254.169.254 straight through it.
// Both dial paths in this tool go through Dialer (D8).
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ErrBlocked is returned for any address or host the guard refuses. Callers
// match on it to distinguish a policy refusal from a transport failure.
var ErrBlocked = errors.New("netguard: blocked")

// imdsAddrs is the fixed set of cloud instance-metadata addresses.
//
// SSRF to cloud IMDS is the textbook credential-exfiltration path for a
// host-resident proxy: an agent that reaches it can harvest IAM credentials,
// instance identity documents, and user-data secrets. They are listed
// explicitly as well as being covered by the link-local and ULA rules, so the
// error message names the actual risk.
var imdsAddrs = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"), // AWS, GCP, Azure IPv4 IMDS
	netip.MustParseAddr("169.254.170.2"),   // AWS ECS task metadata
	netip.MustParseAddr("fd00:ec2::254"),   // AWS IPv6 IMDS
}

// limitedBroadcast is 255.255.255.255. netip.Addr has no IsLimitedBroadcast
// method, so it is checked explicitly.
var limitedBroadcast = netip.MustParseAddr("255.255.255.255")

// reservedPrefixes are ranges Go's netip predicates do not cover but that
// have no legitimate reason to be an upstream target.
//
// 100.64.0.0/10 is the important one: netip's IsPrivate covers only RFC 1918
// and RFC 4193, not RFC 6598 carrier-grade NAT — and Alibaba Cloud's instance
// metadata service lives at 100.100.100.200, inside it. The rest are
// non-forwardable per RFC and are listed so the denial is exhaustive rather
// than incidental.
var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // RFC 6598 carrier-grade NAT
	netip.MustParsePrefix("0.0.0.0/8"),       // RFC 1122 "this network"
	netip.MustParsePrefix("192.0.0.0/24"),    // RFC 6890 IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // RFC 5737 documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // RFC 2544 benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // RFC 5737 documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // RFC 5737 documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // RFC 1112 reserved
	netip.MustParsePrefix("fec0::/10"),       // deprecated IPv6 site-local
	netip.MustParsePrefix("2001:db8::/32"),   // RFC 3849 documentation
}

// embeddedIPv4Prefixes are IPv6 transition prefixes that carry an arbitrary
// IPv4 address inside them.
//
// Go's per-address predicates look only at the outer IPv6 address, so
// 64:ff9b::7f00:1 (NAT64-encoded 127.0.0.1) and 2002:7f00:1:: (6to4-encoded
// 127.0.0.1) both report as ordinary public addresses. On any network with a
// NAT64/DNS64 gateway or 6to4 configured — mobile carriers, some corporate
// VPNs, IPv6-only cloud subnets — a resolver can legitimately return one of
// these for a name that routes to loopback, private, or metadata space. Each
// is decoded and the embedded address re-checked.
var embeddedIPv4Prefixes = []struct {
	prefix netip.Prefix
	// offset is the byte index within the 16-byte address where the embedded
	// IPv4 address begins.
	offset int
}{
	{netip.MustParsePrefix("64:ff9b::/96"), 12},   // RFC 6052 NAT64 well-known
	{netip.MustParsePrefix("64:ff9b:1::/48"), 12}, // RFC 8215 local-use NAT64
	{netip.MustParsePrefix("2002::/16"), 2},       // RFC 3056 6to4
	{netip.MustParsePrefix("2001::/32"), 12},      // RFC 4380 Teredo (client address)
}

// CheckAddr returns a non-nil error if addr must not be dialled.
//
// It is written to be called on an address that DNS has already resolved, so
// hostname-based SSRF ("metadata.google.internal") is covered the same way a
// literal IMDS address is.
func CheckAddr(addr netip.Addr) error {
	if !addr.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlocked)
	}

	// Unmap first: ::ffff:169.254.169.254 must not slip past a comparison
	// written against the v4 form.
	addr = addr.Unmap()

	for _, imds := range imdsAddrs {
		if addr == imds {
			return fmt.Errorf("%w: %s is a cloud instance-metadata address", ErrBlocked, addr)
		}
	}

	// Decode IPv6 transition forms before the predicate checks: the embedded
	// IPv4 address is what the packet actually reaches.
	if inner, ok := embeddedIPv4(addr); ok {
		if err := CheckAddr(inner); err != nil {
			return fmt.Errorf("%s embeds a blocked address: %w", addr, err)
		}
	}

	switch {
	case addr.IsLoopback():
		return fmt.Errorf("%w: %s is a loopback address", ErrBlocked, addr)
	case addr.IsLinkLocalUnicast():
		// Covers 169.254.0.0/16 and fe80::/10, which is where IMDS and most
		// link-local metadata services live.
		return fmt.Errorf("%w: %s is a link-local address", ErrBlocked, addr)
	case addr.IsLinkLocalMulticast():
		return fmt.Errorf("%w: %s is a link-local multicast address", ErrBlocked, addr)
	case addr.IsInterfaceLocalMulticast():
		return fmt.Errorf("%w: %s is an interface-local multicast address", ErrBlocked, addr)
	case addr.IsMulticast():
		return fmt.Errorf("%w: %s is a multicast address", ErrBlocked, addr)
	case addr.IsPrivate():
		// RFC 1918 for v4, RFC 4193 unique-local (fc00::/7) for v6.
		return fmt.Errorf("%w: %s is a private address", ErrBlocked, addr)
	case addr.IsUnspecified():
		// 0.0.0.0 and :: route to loopback at the kernel level.
		return fmt.Errorf("%w: %s is an unspecified address", ErrBlocked, addr)
	case addr == limitedBroadcast:
		return fmt.Errorf("%w: %s is the limited-broadcast address", ErrBlocked, addr)
	}

	// Checked last so the classes above keep their specific messages; this
	// loop catches what netip's predicates do not cover at all.
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(addr) {
			return fmt.Errorf("%w: %s is in the reserved range %s", ErrBlocked, addr, prefix)
		}
	}

	return nil
}

// embeddedIPv4 extracts the IPv4 address carried inside an IPv6 transition
// address, reporting false when addr is not one.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	if !addr.Is6() {
		return netip.Addr{}, false
	}
	for _, e := range embeddedIPv4Prefixes {
		if !e.prefix.Contains(addr) {
			continue
		}
		b := addr.As16()
		inner, ok := netip.AddrFromSlice(b[e.offset : e.offset+4])
		if !ok {
			continue
		}
		// Teredo stores the client's IPv4 address bitwise-complemented.
		if e.prefix.Bits() == 32 && e.prefix.Addr() == netip.MustParseAddr("2001::") {
			c := inner.As4()
			for i := range c {
				c[i] = ^c[i]
			}
			inner = netip.AddrFrom4(c)
		}
		return inner, true
	}
	return netip.Addr{}, false
}

// CheckHost rejects a host that is written in an alternate IP encoding.
//
// Go's resolver will not parse "2130706433" or "0x7f000001" as an address, so
// such a host becomes a DNS lookup that happens to fail. That is an accident
// of resolver behaviour rather than a guarantee: a resolver, a proxy hop, or a
// libc that does accept the form would turn it into a reachable loopback
// address. Rejecting the shape explicitly makes the refusal deliberate.
//
// A host that is already a valid IP literal is left to CheckAddr.
func CheckHost(host string) error {
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrBlocked)
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return nil
	}
	if isNumericHostForm(host) {
		return fmt.Errorf("%w: %q is an IP address written in a non-canonical encoding; write it in dotted-quad or standard IPv6 form", ErrBlocked, host)
	}
	return nil
}

// isNumericHostForm reports whether every dot-separated label of host is a
// decimal, hexadecimal, or octal integer.
//
// No real hostname has this shape — a public TLD is never all digits — so the
// check cannot reject legitimate traffic. It catches "2130706433",
// "0x7f000001", "0177.0.0.1" and short forms such as "127.1".
func isNumericHostForm(host string) bool {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	for _, label := range labels {
		if !isNumericLabel(label) {
			return false
		}
	}
	return true
}

func isNumericLabel(s string) bool {
	if s == "" {
		return false
	}
	if len(s) > 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		for _, c := range s[2:] {
			if !isHexDigit(c) {
				return false
			}
		}
		return true
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isHexDigit(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// Resolver is the subset of *net.Resolver the guard needs. Taking an
// interface lets tests supply hermetic answers, including a resolver whose
// answer changes between calls, without standing up a DNS server.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialFunc dials an already-validated address.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Dialer resolves, validates, and dials upstream addresses.
//
// Both the MITM transport and the raw tunnel relay use one of these, which is
// the whole point: guarding only the transport leaves the tunnel path as an
// open SSRF channel (D8).
type Dialer struct {
	dial     DialFunc
	resolver Resolver
	// exempt holds exact "host:port" strings that skip the address checks.
	// See SetExemptions for why this exists and how narrow it is.
	exempt map[string]struct{}
}

// SetExemptions installs a set of exact "host:port" targets that bypass the
// address checks.
//
// This exists for one reason: every mock upstream a test can start listens on
// loopback, which this package refuses by design. Without a seam, no test
// could exercise any *allowed* path — not a tunnel, not interception, not
// injection — so the only end-to-end coverage possible would be of refusals.
//
// It is deliberately the narrowest thing that works:
//
//   - Exact "host:port" strings only. No globs, no ranges, no address classes.
//   - Not reachable from config.json or rules.json. An operator editing policy
//     cannot turn it on by accident.
//   - The composition root populates it only from a test-only environment
//     variable and logs a warning naming every exemption at startup.
//
// It must never be set in production. The README says so, and the startup
// warning says so again where an operator will actually see it.
func (d *Dialer) SetExemptions(targets []string) {
	if len(targets) == 0 {
		d.exempt = nil
		return
	}
	d.exempt = make(map[string]struct{}, len(targets))
	for _, target := range targets {
		d.exempt[target] = struct{}{}
	}
}

// isExempt reports whether an exact target was exempted.
func (d *Dialer) isExempt(addr string) bool {
	if d.exempt == nil {
		return false
	}
	_, ok := d.exempt[addr]
	return ok
}

// New returns a Dialer wrapping base.
//
// The resolver uses PreferGo so resolution does not go through cgo's
// getaddrinfo and whatever nsswitch modules the host happens to have loaded.
func New(base *net.Dialer) *Dialer {
	if base == nil {
		base = &net.Dialer{}
	}
	return NewWith(base.DialContext, &net.Resolver{PreferGo: true})
}

// NewWith is New with an injectable dial function and resolver, for tests.
func NewWith(dial DialFunc, resolver Resolver) *Dialer {
	return &Dialer{dial: dial, resolver: resolver}
}

// DialContext resolves addr, refuses the dial unless every resolved address
// passes CheckAddr, and then dials a validated address rather than the
// hostname.
//
// Dialling the validated address closes the DNS-rebinding window: if the dial
// were issued against the hostname, the resolver could return a different
// address the second time, and the address actually connected to would be one
// that was never checked.
//
// If any resolved address fails, the whole dial is refused rather than
// falling back to the passing subset — a host that resolves to both a public
// and a private address is either misconfigured or hostile.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("netguard: split host/port %q: %w", addr, err)
	}

	// A test exemption is an exact target match and nothing else.
	if d.isExempt(addr) {
		return d.dial(ctx, network, addr)
	}

	if err := CheckHost(host); err != nil {
		return nil, err
	}

	// An IP literal needs no resolution; check it directly so a rebinding
	// resolver is never consulted for something already concrete.
	if ip, parseErr := netip.ParseAddr(strings.Trim(host, "[]")); parseErr == nil {
		if err := CheckAddr(ip); err != nil {
			return nil, err
		}
		return d.dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
	}

	addrs, err := d.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("netguard: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("netguard: resolve %q: no addresses", host)
	}

	for _, a := range addrs {
		if err := CheckAddr(a); err != nil {
			return nil, fmt.Errorf("netguard: dial %q refused: %w", host, err)
		}
	}

	// Dial the address that was checked, not the name that produced it.
	return d.dial(ctx, network, net.JoinHostPort(addrs[0].Unmap().String(), port))
}
