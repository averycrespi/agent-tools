package netguard_test

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/netguard"
)

// TestBlockPrivate_IMDSUnconditional verifies that IMDS addresses are blocked
// regardless of the allowPrivate flag. SSRF to cloud IMDS is the textbook
// exfil path for cloud-hosted proxies; the block is non-negotiable even when
// allowPrivate=true because legitimate upstreams never need IMDS.
func TestBlockPrivate_IMDSUnconditional(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"aws_gcp_azure_imds_ipv4", "169.254.169.254"},
		{"aws_imds_ipv6", "fd00:ec2::254"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name+"/allowPrivate=false", func(t *testing.T) {
			if err := netguard.BlockPrivate(tc.addr, false); err == nil {
				t.Errorf("BlockPrivate(%q, false): expected error, got nil", tc.addr)
			}
		})
		t.Run(tc.name+"/allowPrivate=true", func(t *testing.T) {
			if err := netguard.BlockPrivate(tc.addr, true); err == nil {
				t.Errorf("BlockPrivate(%q, true): expected error even with allowPrivate=true, got nil", tc.addr)
			}
		})
	}
}

// TestBlockPrivate_RFC1918_Loopback verifies that RFC 1918 and loopback
// addresses are blocked when allowPrivate=false but allowed when allowPrivate=true.
func TestBlockPrivate_RFC1918_Loopback(t *testing.T) {
	cases := []string{
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"127.0.0.1",
		"::1",
	}
	for _, addr := range cases {
		addr := addr
		t.Run(addr+"/allowPrivate=false", func(t *testing.T) {
			if err := netguard.BlockPrivate(addr, false); err == nil {
				t.Errorf("BlockPrivate(%q, false): expected error, got nil", addr)
			}
		})
		t.Run(addr+"/allowPrivate=true", func(t *testing.T) {
			if err := netguard.BlockPrivate(addr, true); err != nil {
				t.Errorf("BlockPrivate(%q, true): expected nil, got %v", addr, err)
			}
		})
	}
}

// TestBlockPrivate_PublicIP verifies that public IPs are always allowed.
func TestBlockPrivate_PublicIP(t *testing.T) {
	cases := []string{
		"8.8.8.8",
		"1.1.1.1",
		"2001:4860:4860::8888",
	}
	for _, addr := range cases {
		addr := addr
		t.Run(addr+"/allowPrivate=false", func(t *testing.T) {
			if err := netguard.BlockPrivate(addr, false); err != nil {
				t.Errorf("BlockPrivate(%q, false): expected nil, got %v", addr, err)
			}
		})
		t.Run(addr+"/allowPrivate=true", func(t *testing.T) {
			if err := netguard.BlockPrivate(addr, true); err != nil {
				t.Errorf("BlockPrivate(%q, true): expected nil, got %v", addr, err)
			}
		})
	}
}

// TestBlockPrivate_InvalidIP verifies that an unparseable address returns an error.
func TestBlockPrivate_InvalidIP(t *testing.T) {
	if err := netguard.BlockPrivate("not-an-ip", false); err == nil {
		t.Error("BlockPrivate(\"not-an-ip\", false): expected error, got nil")
	}
}

// TestBlockPrivate_Unspecified verifies that unspecified addresses (0.0.0.0 and
// ::) are blocked when allowPrivate=false. On Linux, connecting to 0.0.0.0 maps
// to 127.0.0.1 at the kernel level ("0.0.0.0 Day" SSRF). Unspecified is treated
// like loopback: blocked under allowPrivate=false, allowed under allowPrivate=true.
func TestBlockPrivate_Unspecified(t *testing.T) {
	cases := []string{"0.0.0.0", "::"}
	for _, addr := range cases {
		addr := addr
		t.Run(addr+"/allowPrivate=false", func(t *testing.T) {
			if err := netguard.BlockPrivate(addr, false); err == nil {
				t.Errorf("BlockPrivate(%q, false): expected error for unspecified address, got nil", addr)
			}
		})
		t.Run(addr+"/allowPrivate=true", func(t *testing.T) {
			if err := netguard.BlockPrivate(addr, true); err != nil {
				t.Errorf("BlockPrivate(%q, true): expected nil for unspecified address, got %v", addr, err)
			}
		})
	}
}

// TestBlockPrivate_MulticastAndBroadcast verifies that multicast (224.0.0.0/4)
// and the limited-broadcast address (255.255.255.255) are blocked when
// allowPrivate=false.
func TestBlockPrivate_MulticastAndBroadcast(t *testing.T) {
	cases := []string{"224.0.0.1", "255.255.255.255"}
	for _, addr := range cases {
		addr := addr
		t.Run(addr+"/allowPrivate=false", func(t *testing.T) {
			if err := netguard.BlockPrivate(addr, false); err == nil {
				t.Errorf("BlockPrivate(%q, false): expected error, got nil", addr)
			}
		})
	}
}

// TestDialContext_HostnameResolvesToPrivateIP verifies the resolve-then-check
// path: a hostname whose resolved IP is private must be blocked by DialContext
// before any network operation reaches the target.
//
// We use literal IP strings as the host so that net.Resolver.LookupHost returns
// them as-is without a real DNS query — this exercises the full DialContext
// pipeline (split host/port → LookupHost → BlockPrivate) while remaining hermetic.
func TestDialContext_HostnameResolvesToPrivateIP(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{"loopback_ipv4", "127.0.0.1"},
		{"rfc1918_10", "10.0.0.1"},
		{"rfc1918_192168", "192.168.1.1"},
		{"imds_ipv4", "169.254.169.254"},
		{"imds_ipv6", "fd00:ec2::254"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dial := netguard.DialContextWithResolver(
				&net.Dialer{},
				false, // allowPrivate=false: private IPs must be blocked
				net.DefaultResolver,
			)

			addr := net.JoinHostPort(tc.host, "80")
			_, err := dial(context.Background(), "tcp", addr)
			if err == nil {
				t.Fatalf("DialContextWithResolver(%q, allowPrivate=false): expected block error, got nil", addr)
			}
			if !strings.Contains(err.Error(), "netguard:") {
				t.Errorf("DialContextWithResolver(%q): error %q does not look like a netguard block", addr, err.Error())
			}
		})
	}
}

// TestDialContext_IMDSAlwaysBlocked verifies that IMDS addresses are blocked
// even when allowPrivate=true.
func TestDialContext_IMDSAlwaysBlocked(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{"imds_ipv4", "169.254.169.254"},
		{"imds_ipv6", "fd00:ec2::254"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dial := netguard.DialContextWithResolver(
				&net.Dialer{},
				true, // allowPrivate=true: IMDS must still be blocked
				net.DefaultResolver,
			)

			addr := net.JoinHostPort(tc.host, "80")
			_, err := dial(context.Background(), "tcp", addr)
			if err == nil {
				t.Fatalf("DialContextWithResolver(%q, allowPrivate=true): IMDS address must be blocked, got nil", addr)
			}
			if !strings.Contains(err.Error(), "netguard:") {
				t.Errorf("DialContextWithResolver(%q): error %q does not look like a netguard block", addr, err.Error())
			}
		})
	}
}

// TestDialContext_PublicIPPassedThrough verifies that a hostname that resolves
// to a public IP is passed through to the underlying dialer. We listen on a
// random loopback port using allowPrivate=true so that 127.0.0.1 is permitted,
// which lets us confirm the base dialer was actually reached without hitting
// the public internet.
func TestDialContext_PublicIPPassedThrough(t *testing.T) {
	// Stand up a TCP listener on loopback to prove the base dialer is called.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Accept one connection asynchronously so the dial doesn't hang.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}()

	dial := netguard.DialContextWithResolver(
		&net.Dialer{},
		true, // allowPrivate=true so loopback is permitted
		net.DefaultResolver,
	)

	// 127.0.0.1 is a literal IP; LookupHost returns it as-is.
	addr := ln.Addr().String()
	conn, err := dial(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("DialContextWithResolver(%q, allowPrivate=true): expected success, got %v", addr, err)
	}
	_ = conn.Close()
}

// TestDialContext_DialsByIP is a regression test for Issue 1 (TOCTOU / DNS
// rebinding). It verifies that after resolving a hostname the guard passes an
// IP-literal address — not the original hostname — to the base dialer.
//
// The indirect proof: we give the guard a fake resolver that maps any hostname
// to 127.0.0.1, and we set up a TCP listener on 127.0.0.1. The dial succeeds
// only if the guard actually connects to 127.0.0.1 (IP-literal) rather than
// re-resolving "example.com" via system DNS (which would reach a real server,
// not our loopback listener).
func TestDialContext_DialsByIP(t *testing.T) {
	// Stand up a listener on loopback; the dial must reach this listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	_, port, _ := net.SplitHostPort(ln.Addr().String())

	// Fake resolver: maps any hostname to 127.0.0.1.
	resolver := newFakeResolver([]string{"127.0.0.1"})

	dial := netguard.DialContextWithResolver(
		&net.Dialer{},
		true, // allowPrivate=true: loopback is allowed
		resolver,
	)

	// Dial a non-loopback hostname at our loopback port. If the guard
	// re-resolves via system DNS it will connect to a real server (or fail),
	// not to our listener. Success here proves IP-literal dialling.
	conn, err := dial(context.Background(), "tcp", net.JoinHostPort("example.com", port))
	if err != nil {
		t.Fatalf("DialContextWithResolver: expected success (guard should dial by IP), got %v", err)
	}
	_ = conn.Close()
}

// TestDialContext_MixedPublicPrivateBlocked verifies that if a hostname
// resolves to both a public IP and a private IP the entire dial is rejected
// (Issue 1 / DNS rebinding guard). The guard must not dial only the "safe"
// subset — an upstream resolving to public+private is malicious or misconfigured.
func TestDialContext_MixedPublicPrivateBlocked(t *testing.T) {
	// Fake resolver: returns one public IP and one private IP.
	resolver := newFakeResolver([]string{"8.8.8.8", "10.0.0.1"})

	dial := netguard.DialContextWithResolver(
		&net.Dialer{},
		false, // allowPrivate=false
		resolver,
	)

	_, err := dial(context.Background(), "tcp", "example.com:443")
	if err == nil {
		t.Fatal("DialContextWithResolver: expected block for mixed public+private resolution, got nil")
	}
	if !strings.Contains(err.Error(), "netguard:") {
		t.Errorf("error %q does not look like a netguard block", err.Error())
	}
}

// newFakeResolver returns a *net.Resolver that answers any LookupHost with the
// given fixed list of IPs. It works by starting a minimal UDP DNS stub on
// loopback and pointing the resolver's Dial hook at it. Only A records are
// served (IPv4 IPs in the fixed list); AAAA and other qtypes receive NXDOMAIN.
func newFakeResolver(ips []string) *net.Resolver {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		panic("netguard test: listen UDP: " + err.Error())
	}
	go serveFakeDNS(pc, ips)
	addr := pc.LocalAddr().String()
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp", addr)
		},
	}
}

// serveFakeDNS reads DNS queries from pc and responds with A records for the
// given fixed IP list. It exits when pc is closed.
func serveFakeDNS(pc net.PacketConn, ips []string) {
	defer func() { _ = pc.Close() }()
	buf := make([]byte, 512)
	for {
		n, src, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		resp := buildDNSResponse(pkt, ips)
		if resp != nil {
			_, _ = pc.WriteTo(resp, src)
		}
	}
}

// buildDNSResponse parses a raw DNS query and builds an A-record response
// containing each IPv4 address in ips. Uses only stdlib (manual DNS wire
// format) to avoid external dependencies.
func buildDNSResponse(query []byte, ips []string) []byte {
	if len(query) < 12 {
		return nil
	}

	id := query[0:2]
	qdCount := int(query[4])<<8 | int(query[5])
	if qdCount == 0 {
		return nil
	}

	// Walk past the QNAME label sequence.
	offset := 12
	for offset < len(query) {
		length := int(query[offset])
		if length == 0 {
			offset++
			break
		}
		if length >= 0xC0 { // pointer — unexpected in queries
			offset += 2
			break
		}
		if offset+1+length > len(query) {
			return nil
		}
		offset += 1 + length
	}
	if offset+4 > len(query) {
		return nil
	}
	qtype := int(query[offset])<<8 | int(query[offset+1])
	offset += 4 // consume QTYPE + QCLASS

	questionSection := query[12:offset]

	// Collect IPv4 A records to serve.
	var rrs []net.IP
	if qtype == 1 { // A
		for _, s := range ips {
			ip := net.ParseIP(s)
			if ip == nil {
				continue
			}
			ip4 := ip.To4()
			if ip4 != nil {
				rrs = append(rrs, ip4)
			}
		}
	}

	if len(rrs) == 0 {
		return buildNXDomain(id, questionSection)
	}

	var resp []byte
	resp = append(resp, id...)
	resp = append(resp, 0x81, 0x80)                        // QR=1 AA=0 RA=1 RCODE=0
	resp = append(resp, 0x00, 0x01)                        // QDCOUNT=1
	resp = append(resp, byte(len(rrs)>>8), byte(len(rrs))) // ANCOUNT
	resp = append(resp, 0x00, 0x00)                        // NSCOUNT
	resp = append(resp, 0x00, 0x00)                        // ARCOUNT
	resp = append(resp, questionSection...)

	for _, ip4 := range rrs {
		resp = append(resp, 0xC0, 0x0C)             // NAME: pointer to offset 12
		resp = append(resp, 0x00, 0x01)             // TYPE A
		resp = append(resp, 0x00, 0x01)             // CLASS IN
		resp = append(resp, 0x00, 0x00, 0x00, 0x3C) // TTL 60
		resp = append(resp, 0x00, 0x04)             // RDLENGTH 4
		resp = append(resp, ip4...)
	}
	return resp
}

// buildNXDomain returns a minimal NXDOMAIN response reusing the question section.
func buildNXDomain(id, question []byte) []byte {
	var resp []byte
	resp = append(resp, id...)
	resp = append(resp, 0x81, 0x83) // QR=1 RCODE=3 (NXDOMAIN)
	resp = append(resp, 0x00, 0x01) // QDCOUNT
	resp = append(resp, 0x00, 0x00) // ANCOUNT
	resp = append(resp, 0x00, 0x00) // NSCOUNT
	resp = append(resp, 0x00, 0x00) // ARCOUNT
	resp = append(resp, question...)
	return resp
}
