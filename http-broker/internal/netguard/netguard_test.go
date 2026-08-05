package netguard_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/netguard"
)

// TestCheckAddrBlocked is one case per address class AC-11 names, plus the
// IPv4-mapped spellings of each.
func TestCheckAddrBlocked(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		// Cloud metadata.
		{"aws imds v4", "169.254.169.254", "instance-metadata"},
		{"aws ecs task metadata", "169.254.170.2", "instance-metadata"},
		{"aws imds v6", "fd00:ec2::254", "instance-metadata"},

		// Loopback.
		{"ipv4 loopback", "127.0.0.1", "loopback"},
		{"ipv4 loopback elsewhere in /8", "127.1.2.3", "loopback"},
		{"ipv6 loopback", "::1", "loopback"},

		// Private / RFC 1918.
		{"rfc1918 10/8", "10.0.0.1", "private"},
		{"rfc1918 172.16/12", "172.16.0.1", "private"},
		{"rfc1918 192.168/16", "192.168.1.1", "private"},

		// IPv6 unique-local (ULA, fc00::/7).
		{"ipv6 ula fc00", "fc00::1", "private"},
		{"ipv6 ula fd00", "fd12:3456:789a::1", "private"},

		// Link-local.
		{"ipv4 link-local", "169.254.1.1", "link-local"},
		{"ipv6 link-local", "fe80::1", "link-local"},

		// Unspecified: 0.0.0.0 and :: route to loopback at the kernel level.
		{"ipv4 unspecified", "0.0.0.0", "unspecified"},
		{"ipv6 unspecified", "::", "unspecified"},

		// Multicast and broadcast.
		{"ipv4 multicast", "224.0.0.1", "multicast"},
		{"ipv6 multicast", "ff0e::1", "multicast"},
		{"ipv6 link-local multicast", "ff02::1", "multicast"},
		{"limited broadcast", "255.255.255.255", "broadcast"},

		// IPv4-mapped IPv6: each blocked address in a second spelling.
		{"mapped loopback", "::ffff:127.0.0.1", "loopback"},
		{"mapped imds", "::ffff:169.254.169.254", "instance-metadata"},
		{"mapped rfc1918", "::ffff:10.0.0.1", "private"},
		{"mapped unspecified", "::ffff:0.0.0.0", "unspecified"},
		{"mapped link-local", "::ffff:169.254.1.1", "link-local"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := netip.ParseAddr(tc.addr)
			if err != nil {
				t.Fatalf("ParseAddr(%q): %v", tc.addr, err)
			}
			err = netguard.CheckAddr(addr)
			if err == nil {
				t.Fatalf("CheckAddr(%s) = nil, want it blocked", tc.addr)
			}
			if !errors.Is(err, netguard.ErrBlocked) {
				t.Fatalf("CheckAddr(%s) error %v, want it to wrap ErrBlocked", tc.addr, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("CheckAddr(%s) error %q should describe the class %q", tc.addr, err, tc.want)
			}
		})
	}
}

// TestCheckAddrEmbeddedIPv4 covers the IPv6 transition prefixes that carry an
// arbitrary IPv4 address. Go's per-address predicates see only the outer IPv6
// address, so without decoding, 64:ff9b::7f00:1 reads as an ordinary public
// address while actually routing to 127.0.0.1 through a NAT64 gateway.
func TestCheckAddrEmbeddedIPv4(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"nat64 loopback", "64:ff9b::7f00:1"},
		{"nat64 imds", "64:ff9b::a9fe:a9fe"},
		{"nat64 rfc1918", "64:ff9b::a00:1"},
		{"nat64 local-use loopback", "64:ff9b:1::7f00:1"},
		{"6to4 loopback", "2002:7f00:1::"},
		{"6to4 imds", "2002:a9fe:a9fe::"},
		{"6to4 rfc1918", "2002:c0a8:101::"},
		{"teredo loopback", "2001:0:0:0:0:0:80ff:fffe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tc.addr)
			err := netguard.CheckAddr(addr)
			if err == nil {
				t.Fatalf("CheckAddr(%s) = nil, want it blocked: it embeds a blocked IPv4 address", tc.addr)
			}
			if !errors.Is(err, netguard.ErrBlocked) {
				t.Fatalf("CheckAddr(%s) error %v, want it to wrap ErrBlocked", tc.addr, err)
			}
			if !strings.Contains(err.Error(), "embeds") {
				t.Errorf("CheckAddr(%s) error %q should say the address embeds a blocked one", tc.addr, err)
			}
		})
	}
}

// TestCheckAddrEmbeddedPublicIPv4IsAllowed guards against over-blocking: a
// transition address wrapping a public IPv4 address is legitimate.
func TestCheckAddrEmbeddedPublicIPv4IsAllowed(t *testing.T) {
	for _, s := range []string{
		"64:ff9b::5db8:d822", // NAT64 of 93.184.216.34
		"2002:5db8:d822::",   // 6to4 of 93.184.216.34
	} {
		if err := netguard.CheckAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("CheckAddr(%s) = %v, want allowed: it embeds a public address", s, err)
		}
	}
}

// TestCheckAddrReservedRanges covers ranges netip's predicates do not.
// 100.64.0.0/10 is the one that matters most: Alibaba Cloud's metadata
// service sits at 100.100.100.200, and IsPrivate does not cover RFC 6598.
func TestCheckAddrReservedRanges(t *testing.T) {
	cases := []struct{ name, addr string }{
		{"cgnat alibaba imds", "100.100.100.200"},
		{"cgnat low", "100.64.0.1"},
		{"cgnat high", "100.127.255.255"},
		{"this network", "0.1.2.3"},
		{"ietf protocol assignments", "192.0.0.1"},
		{"benchmarking", "198.18.0.1"},
		{"reserved class e", "240.0.0.1"},
		{"documentation v4", "192.0.2.1"},
		{"ipv6 site-local", "fec0::1"},
		{"ipv6 documentation", "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := netguard.CheckAddr(netip.MustParseAddr(tc.addr))
			if err == nil {
				t.Fatalf("CheckAddr(%s) = nil, want it blocked", tc.addr)
			}
			if !errors.Is(err, netguard.ErrBlocked) {
				t.Fatalf("CheckAddr(%s) error %v, want it to wrap ErrBlocked", tc.addr, err)
			}
		})
	}
}

func TestCheckAddrReservedBoundaries(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"100.63.255.255", false},
		{"100.64.0.0", true},
		{"100.127.255.255", true},
		{"100.128.0.0", false},

		{"198.17.255.255", false},
		{"198.18.0.0", true},
		{"198.19.255.255", true},
		{"198.20.0.0", false},

		{"239.255.255.255", true}, // multicast
		{"240.0.0.0", true},       // reserved
	}
	for _, tc := range cases {
		err := netguard.CheckAddr(netip.MustParseAddr(tc.addr))
		if tc.blocked && err == nil {
			t.Errorf("CheckAddr(%s) = nil, want blocked", tc.addr)
		}
		if !tc.blocked && err != nil {
			t.Errorf("CheckAddr(%s) = %v, want allowed", tc.addr, err)
		}
	}
}

func TestCheckAddrAllowed(t *testing.T) {
	allowed := []string{
		"93.184.216.34",
		"140.82.121.6",
		"1.1.1.1",
		"8.8.8.8",
		"2606:2800:220:1:248:1893:25c8:1946",
		"2001:4860:4860::8888",
		"172.15.0.1", // just below the 172.16/12 block
		"172.32.0.1", // just above it
		"11.0.0.1",   // just above 10/8
		"9.255.255.255",
	}
	for _, s := range allowed {
		if err := netguard.CheckAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("CheckAddr(%s) = %v, want nil: this is a routable public address", s, err)
		}
	}
}

// TestCheckAddrBoundaries pins the edge of each blocked range so a later
// refactor cannot quietly widen or narrow one.
func TestCheckAddrBoundaries(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"126.255.255.255", false},
		{"127.0.0.0", true},
		{"127.255.255.255", true},
		{"128.0.0.0", false},

		{"169.253.255.255", false},
		{"169.254.0.0", true},
		{"169.254.255.255", true},
		{"169.255.0.0", false},

		{"172.15.255.255", false},
		{"172.16.0.0", true},
		{"172.31.255.255", true},
		{"172.32.0.0", false},

		{"192.167.255.255", false},
		{"192.168.0.0", true},
		{"192.168.255.255", true},
		{"192.169.0.0", false},

		{"223.255.255.255", false},
		{"224.0.0.0", true},
		{"239.255.255.255", true},

		{"fbff::1", false},
		{"fc00::", true},
		{"fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"fe00::1", false},
	}
	for _, tc := range cases {
		err := netguard.CheckAddr(netip.MustParseAddr(tc.addr))
		if tc.blocked && err == nil {
			t.Errorf("CheckAddr(%s) = nil, want blocked", tc.addr)
		}
		if !tc.blocked && err != nil {
			t.Errorf("CheckAddr(%s) = %v, want allowed", tc.addr, err)
		}
	}
}

func TestCheckAddrRejectsInvalid(t *testing.T) {
	if err := netguard.CheckAddr(netip.Addr{}); err == nil {
		t.Error("CheckAddr on the zero Addr = nil, want an error")
	}
}

// TestCheckHostAlternateEncodings covers AC-11's alternate IP-literal
// encodings. Each is a spelling of a blocked address that some resolver
// stacks accept.
func TestCheckHostAlternateEncodings(t *testing.T) {
	blocked := []string{
		"2130706433",   // decimal 127.0.0.1
		"0x7f000001",   // hex
		"0X7F000001",   // hex, uppercase
		"017700000001", // octal
		"127.1",        // short dotted form
		"127.0.1",
		"0177.0.0.1", // octal-dotted
		"3232235777", // decimal 192.168.1.1
		"0xa000001",  // hex 10.0.0.1
		"2852039166", // decimal 169.254.169.254
	}
	for _, host := range blocked {
		err := netguard.CheckHost(host)
		if err == nil {
			t.Errorf("CheckHost(%q) = nil, want it blocked as a non-canonical IP encoding", host)
			continue
		}
		if !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("CheckHost(%q) error %v, want it to wrap ErrBlocked", host, err)
		}
	}
}

func TestCheckHostAllowsRealHostnames(t *testing.T) {
	allowed := []string{
		"api.github.com",
		"example.com",
		"3com.com",              // leading digits in a label
		"7-eleven.com",          // digit then hyphen
		"xn--bcher-kva.example", // punycode
		"a1.b2.example.com",
		"93.184.216.34", // canonical literal: CheckAddr's job, not CheckHost's
		"::1",
		"[::1]",
	}
	for _, host := range allowed {
		if err := netguard.CheckHost(host); err != nil {
			t.Errorf("CheckHost(%q) = %v, want nil", host, err)
		}
	}
}

func TestCheckHostRejectsEmpty(t *testing.T) {
	if err := netguard.CheckHost(""); err == nil {
		t.Error(`CheckHost("") = nil, want an error`)
	}
}

// TestDialRefusesBlockedLiteral proves the guard fires on the dial path and
// that nothing reaches the network.
func TestDialRefusesBlockedLiteral(t *testing.T) {
	for _, target := range []string{
		"169.254.169.254:80",
		"127.0.0.1:80",
		"[::1]:80",
		"10.0.0.1:443",
		"[::ffff:127.0.0.1]:80",
		"0.0.0.0:80",
		"2130706433:80",
		"0x7f000001:80",
	} {
		rec := &recordingDialer{}
		d := netguard.NewWith(rec.dial, staticResolver{})

		_, err := d.DialContext(context.Background(), "tcp", target)
		if err == nil {
			t.Errorf("DialContext(%q) = nil error, want it refused", target)
			continue
		}
		if !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("DialContext(%q) error %v, want it to wrap ErrBlocked", target, err)
		}
		if got := rec.calls.Load(); got != 0 {
			t.Errorf("DialContext(%q) dialled %d times, want 0: a refused target must never reach the network", target, got)
		}
	}
}

func TestDialAllowsPublicLiteral(t *testing.T) {
	rec := &recordingDialer{}
	d := netguard.NewWith(rec.dial, staticResolver{})

	if _, err := d.DialContext(context.Background(), "tcp", "93.184.216.34:443"); err != nil {
		t.Fatalf("DialContext on a public literal = %v, want it allowed", err)
	}
	if got := rec.last(); got != "93.184.216.34:443" {
		t.Errorf("dialled %q, want the validated literal", got)
	}
}

// TestDialUnmapsBeforeDialling proves a mapped spelling of a public address
// is dialled in its canonical form rather than being passed through.
func TestDialUnmapsBeforeDialling(t *testing.T) {
	rec := &recordingDialer{}
	d := netguard.NewWith(rec.dial, staticResolver{})

	if _, err := d.DialContext(context.Background(), "tcp", "[::ffff:93.184.216.34]:443"); err != nil {
		t.Fatalf("DialContext = %v, want it allowed", err)
	}
	if got := rec.last(); got != "93.184.216.34:443" {
		t.Errorf("dialled %q, want the unmapped form 93.184.216.34:443", got)
	}
}

func TestDialResolvesAndDialsValidatedAddress(t *testing.T) {
	rec := &recordingDialer{}
	resolver := staticResolver{"public.test": {"93.184.216.34"}}
	d := netguard.NewWith(rec.dial, resolver)

	if _, err := d.DialContext(context.Background(), "tcp", "public.test:443"); err != nil {
		t.Fatalf("DialContext = %v, want it allowed", err)
	}
	// The dialled address must be the resolved IP, not the hostname: dialling
	// the name would let the resolver answer differently the second time.
	if got := rec.last(); got != "93.184.216.34:443" {
		t.Errorf("dialled %q, want the resolved address 93.184.216.34:443", got)
	}
}

func TestDialRefusesResolvedBlockedAddress(t *testing.T) {
	rec := &recordingDialer{}
	resolver := staticResolver{"metadata.google.internal": {"169.254.169.254"}}
	d := netguard.NewWith(rec.dial, resolver)

	_, err := d.DialContext(context.Background(), "tcp", "metadata.google.internal:80")
	if err == nil {
		t.Fatal("a name resolving to IMDS should be refused")
	}
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("error %v, want it to wrap ErrBlocked", err)
	}
	if got := rec.calls.Load(); got != 0 {
		t.Errorf("dialled %d times, want 0", got)
	}
}

// TestDialRefusesWhenAnyResolvedAddressIsBlocked is the split-horizon case: a
// name answering with one public and one loopback address is refused whole,
// never dialled on the passing subset.
func TestDialRefusesWhenAnyResolvedAddressIsBlocked(t *testing.T) {
	for _, answer := range [][]string{
		{"93.184.216.34", "127.0.0.1"},
		{"127.0.0.1", "93.184.216.34"},
	} {
		rec := &recordingDialer{}
		d := netguard.NewWith(rec.dial, staticResolver{"split.test": answer})

		_, err := d.DialContext(context.Background(), "tcp", "split.test:80")
		if err == nil {
			t.Fatalf("answer %v: a name resolving to both public and loopback should be refused", answer)
		}
		if !errors.Is(err, netguard.ErrBlocked) {
			t.Fatalf("answer %v: error %v, want it to wrap ErrBlocked", answer, err)
		}
		if got := rec.calls.Load(); got != 0 {
			t.Errorf("answer %v: dialled %d times, want 0", answer, got)
		}
	}
}

// TestDialRebinding is AC-11's DNS-rebinding case. The resolver's answer
// flips from public to loopback between calls; the guard must resolve exactly
// once and dial the address it validated, so the second answer can never be
// the one connected to.
func TestDialRebinding(t *testing.T) {
	rec := &recordingDialer{}
	flipping := &flippingResolver{answers: [][]string{
		{"93.184.216.34"}, // checked
		{"127.0.0.1"},     // what a second lookup would return
	}}
	d := netguard.NewWith(rec.dial, flipping)

	if _, err := d.DialContext(context.Background(), "tcp", "rebind.test:443"); err != nil {
		t.Fatalf("DialContext = %v, want the first (public) answer to be dialled", err)
	}
	if got := flipping.calls.Load(); got != 1 {
		t.Errorf("resolver consulted %d times, want exactly 1: a second lookup reopens the rebinding window", got)
	}
	if got := rec.last(); got != "93.184.216.34:443" {
		t.Errorf("dialled %q, want the validated address 93.184.216.34:443 rather than the rebound 127.0.0.1", got)
	}
}

func TestDialRejectsMalformedTarget(t *testing.T) {
	rec := &recordingDialer{}
	d := netguard.NewWith(rec.dial, staticResolver{})

	if _, err := d.DialContext(context.Background(), "tcp", "no-port"); err == nil {
		t.Error("DialContext on an address with no port = nil, want an error")
	}
}

func TestDialRejectsEmptyResolverAnswer(t *testing.T) {
	rec := &recordingDialer{}
	d := netguard.NewWith(rec.dial, staticResolver{"empty.test": {}})

	if _, err := d.DialContext(context.Background(), "tcp", "empty.test:443"); err == nil {
		t.Error("DialContext with an empty resolver answer = nil, want an error")
	}
	if got := rec.calls.Load(); got != 0 {
		t.Errorf("dialled %d times, want 0", got)
	}
}

// TestDialFallsBackToTheNextValidatedAddress is the dual-stack case.
//
// LookupNetIP("ip") returns AAAA and A records together, so on an IPv4-only
// host the first address is unreachable. Stopping there failed every request
// to a dual-stack upstream with a validated, reachable address in hand. Every
// address has already passed CheckAddr, so trying the rest gives up nothing.
func TestDialFallsBackToTheNextValidatedAddress(t *testing.T) {
	rec := &failingDialer{failAddrs: map[string]bool{"[2606:2800:220:1:248:1893:25c8:1946]:443": true}}
	d := netguard.NewWith(rec.dial, staticResolver{
		"dual.test": {"2606:2800:220:1:248:1893:25c8:1946", "93.184.216.34"},
	})

	if _, err := d.DialContext(context.Background(), "tcp", "dual.test:443"); err != nil {
		t.Fatalf("DialContext = %v, want the IPv4 address tried after the IPv6 one failed", err)
	}
	if got := rec.last(); got != "93.184.216.34:443" {
		t.Errorf("dialled %q last, want the fallback address 93.184.216.34:443", got)
	}
}

// TestDialReportsTheFailureWhenEveryAddressFails keeps the fallback from
// swallowing a genuinely unreachable upstream.
func TestDialReportsTheFailureWhenEveryAddressFails(t *testing.T) {
	rec := &failingDialer{failAll: true}
	d := netguard.NewWith(rec.dial, staticResolver{
		"dead.test": {"93.184.216.34", "93.184.216.35"},
	})

	if _, err := d.DialContext(context.Background(), "tcp", "dead.test:443"); err == nil {
		t.Error("DialContext = nil, want the dial failure reported")
	}
	if got := rec.calls.Load(); got != 2 {
		t.Errorf("dialled %d times, want every validated address tried", got)
	}
}

// --- test doubles -----------------------------------------------------------

// failingDialer fails the addresses it is told to and records every attempt.
type failingDialer struct {
	failAll   bool
	failAddrs map[string]bool

	calls atomic.Int64
	addr  atomic.Value
}

func (f *failingDialer) dial(_ context.Context, _, addr string) (net.Conn, error) {
	f.calls.Add(1)
	f.addr.Store(addr)
	if f.failAll || f.failAddrs[addr] {
		return nil, errors.New("connect: network is unreachable")
	}
	return nil, nil //nolint:nilnil // the tests assert on the recorded address, not on a connection
}

func (f *failingDialer) last() string {
	v, _ := f.addr.Load().(string)
	return v
}

type recordingDialer struct {
	calls atomic.Int64
	addr  atomic.Value
}

func (r *recordingDialer) dial(_ context.Context, _, addr string) (net.Conn, error) {
	r.calls.Add(1)
	r.addr.Store(addr)
	return nil, nil //nolint:nilnil // the tests assert on the recorded address, not on a connection
}

func (r *recordingDialer) last() string {
	v, _ := r.addr.Load().(string)
	return v
}

// staticResolver answers from a fixed table.
type staticResolver map[string][]string

func (s staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	answer, ok := s[host]
	if !ok {
		return nil, fmt.Errorf("no such host %q", host)
	}
	return parseAll(answer), nil
}

// flippingResolver returns a different answer on each call, standing in for a
// rebinding authoritative server.
type flippingResolver struct {
	calls   atomic.Int64
	answers [][]string
}

func (f *flippingResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	n := f.calls.Add(1)
	idx := int(n) - 1
	if idx >= len(f.answers) {
		idx = len(f.answers) - 1
	}
	return parseAll(f.answers[idx]), nil
}

func parseAll(ss []string) []netip.Addr {
	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

// TestDialAllowPrivatePermitsPrivateAddress is the internal-service case: a
// host a rule marked allow_private may resolve into RFC 1918 space, and the
// dial still goes to the validated address rather than the name.
func TestDialAllowPrivatePermitsPrivateAddress(t *testing.T) {
	rec := &recordingDialer{}
	d := netguard.NewWith(rec.dial, staticResolver{"internal.test": {"10.0.0.7"}})

	ctx := netguard.WithAllowPrivate(context.Background(), true)
	if _, err := d.DialContext(ctx, "tcp", "internal.test:443"); err != nil {
		t.Fatalf("DialContext = %v, want a private address allowed under allow_private", err)
	}
	if got := rec.last(); got != "10.0.0.7:443" {
		t.Errorf("dialled %q, want the resolved address 10.0.0.7:443", got)
	}
}

// TestDialAllowPrivateStillRefusesOtherClasses pins the width of the grant:
// allow_private relaxes RFC 1918 and RFC 4193 only. Every other class stays
// refused, or the flag would be an SSRF off switch.
func TestDialAllowPrivateStillRefusesOtherClasses(t *testing.T) {
	for name, addr := range map[string]string{
		"loopback":       "127.0.0.1",
		"link-local":     "169.254.1.1",
		"imds":           "169.254.169.254",
		"ecs-metadata":   "169.254.170.2",
		"ipv6-imds":      "fd00:ec2::254",
		"cgnat-reserved": "100.100.100.200",
		"unspecified":    "0.0.0.0",
		"nat64-loopback": "64:ff9b::7f00:1",
	} {
		rec := &recordingDialer{}
		d := netguard.NewWith(rec.dial, staticResolver{"internal.test": {addr}})

		ctx := netguard.WithAllowPrivate(context.Background(), true)
		_, err := d.DialContext(ctx, "tcp", "internal.test:443")
		if err == nil {
			t.Errorf("%s (%s): allowed, want it refused even under allow_private", name, addr)
			continue
		}
		if !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("%s (%s): error %v, want it to wrap ErrBlocked", name, addr, err)
		}
		if got := rec.calls.Load(); got != 0 {
			t.Errorf("%s (%s): dialled %d times, want 0", name, addr, got)
		}
	}
}

// TestDialWithoutAllowPrivateRefusesPrivateAddress is the default: absent the
// flag, nothing changes. This is the case that produced the original
// private-address block.
func TestDialWithoutAllowPrivateRefusesPrivateAddress(t *testing.T) {
	rec := &recordingDialer{}
	d := netguard.NewWith(rec.dial, staticResolver{"internal.test": {"10.0.0.7"}})

	_, err := d.DialContext(context.Background(), "tcp", "internal.test:443")
	if err == nil {
		t.Fatal("a private address should be refused without allow_private")
	}
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("error %v, want it to wrap ErrBlocked", err)
	}
}

// TestDialAllowPrivateAppliesToIPv6ULA covers the v6 half of IsPrivate, which
// is fc00::/7 rather than an RFC 1918 range.
func TestDialAllowPrivateAppliesToIPv6ULA(t *testing.T) {
	rec := &recordingDialer{}
	d := netguard.NewWith(rec.dial, staticResolver{"internal.test": {"fd12:3456::1"}})

	ctx := netguard.WithAllowPrivate(context.Background(), true)
	if _, err := d.DialContext(ctx, "tcp", "internal.test:443"); err != nil {
		t.Fatalf("DialContext = %v, want a ULA address allowed under allow_private", err)
	}
}

// TestDialAllowPrivateWithIPLiteral covers the literal path, which skips
// resolution and checks the address directly.
func TestDialAllowPrivateWithIPLiteral(t *testing.T) {
	rec := &recordingDialer{}
	d := netguard.NewWith(rec.dial, staticResolver{})

	ctx := netguard.WithAllowPrivate(context.Background(), true)
	if _, err := d.DialContext(ctx, "tcp", "10.0.0.7:443"); err != nil {
		t.Fatalf("DialContext = %v, want a private literal allowed under allow_private", err)
	}
}
