// Package netguard guards upstream dials against SSRF and IMDS abuse.
package netguard

import (
	"context"
	"fmt"
	"net"
)

// imdsAddrs is the fixed set of cloud IMDS addresses that must never be
// reachable through the proxy, even when allow_private_upstream=true.
//
// WHY: SSRF to cloud IMDS (169.254.169.254 for AWS/GCP/Azure, fd00:ec2::254
// for AWS IPv6) is the textbook credential-exfiltration path for cloud-hosted
// proxies. An agent that can reach IMDS can harvest IAM credentials, instance
// identity documents, and user-data secrets from the host. The unconditional
// block is non-negotiable even under allow_private_upstream=true because
// legitimate upstream services never live at an IMDS address — the only
// reason an agent would dial it is to exploit the proxy's network position.
var imdsAddrs = []net.IP{
	net.ParseIP("169.254.169.254"), // AWS, GCP, Azure IPv4 IMDS
	net.ParseIP("fd00:ec2::254"),   // AWS IPv6 IMDS
}

// limitedBroadcast is 255.255.255.255, the IPv4 limited-broadcast address.
// net.IP has no IsLimitedBroadcast method, so we check it explicitly.
var limitedBroadcast = net.ParseIP("255.255.255.255")

// BlockPrivate returns a non-nil error if addr (a bare IP string, no port)
// should be blocked. It is designed to be called after DNS resolution so that
// hostname-based SSRF (e.g. "metadata.google.internal") is covered too.
//
// Blocking rules (in priority order):
//  1. IMDS addresses are always blocked, regardless of allowPrivate.
//  2. Loopback, link-local unicast, RFC 1918/4193 private addresses,
//     unspecified (0.0.0.0 / ::), multicast, and limited broadcast are
//     blocked when allowPrivate is false.
//  3. All other addresses are allowed.
func BlockPrivate(addr string, allowPrivate bool) error {
	ip := net.ParseIP(addr)
	if ip == nil {
		return fmt.Errorf("netguard: cannot parse IP address %q", addr)
	}

	// Rule 1: IMDS — unconditional block, even with allowPrivate=true.
	for _, imds := range imdsAddrs {
		if ip.Equal(imds) {
			return fmt.Errorf("netguard: %s is an IMDS address and is always blocked", addr)
		}
	}

	if allowPrivate {
		return nil
	}

	// Rule 2: loopback, link-local, private, unspecified, multicast, broadcast.
	if ip.IsLoopback() {
		return fmt.Errorf("netguard: %s is a loopback address", addr)
	}
	if ip.IsLinkLocalUnicast() {
		return fmt.Errorf("netguard: %s is a link-local address", addr)
	}
	if ip.IsPrivate() {
		return fmt.Errorf("netguard: %s is a private address", addr)
	}
	// Issue 2: 0.0.0.0 / :: map to loopback at the kernel level ("0.0.0.0 Day").
	if ip.IsUnspecified() {
		return fmt.Errorf("netguard: %s is an unspecified address", addr)
	}
	// Issue 3: multicast (224.0.0.0/4) and limited broadcast (255.255.255.255).
	if ip.IsMulticast() {
		return fmt.Errorf("netguard: %s is a multicast address", addr)
	}
	if ip.Equal(limitedBroadcast) {
		return fmt.Errorf("netguard: %s is the limited-broadcast address", addr)
	}

	return nil
}

// DialContext wraps baseDialer.DialContext with an SSRF/IMDS guard. It
// resolves the target hostname to IPs first, then checks each resolved IP
// with BlockPrivate before dialling. All resolved IPs must pass; if any
// fails the dial is rejected.
//
// The resolver is constructed with PreferGo=true to avoid cgo's
// getaddrinfo / nsswitch.conf path, which could be influenced by a hostile
// nsswitch module (Issue 4).
//
// WHY: Go's net.Dialer resolves hostnames internally and does not expose the
// resolved IPs to the caller. Hooking after resolution — by resolving first
// ourselves — ensures hostname-based SSRF (e.g. "metadata.google.internal")
// is caught the same way as a literal IMDS IP.
func DialContext(baseDialer *net.Dialer, allowPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	// Issue 4: use a Go-native resolver (PreferGo=true) to avoid cgo
	// getaddrinfo and hostile nsswitch.conf modules.
	r := &net.Resolver{PreferGo: true}
	return DialContextWithResolver(baseDialer, allowPrivate, r)
}

// DialContextWithResolver is like DialContext but uses the supplied resolver
// instead of a dedicated Go-native resolver. It exists primarily to support
// hermetic testing with a custom DNS hook; production code should use DialContext.
func DialContextWithResolver(baseDialer *net.Dialer, allowPrivate bool, resolver *net.Resolver) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("netguard: split host/port %q: %w", addr, err)
		}

		// Resolve host to IPs. We use the supplied resolver so that
		// /etc/hosts and system DNS both apply for the default case,
		// and hermetic test resolvers can be injected for unit tests.
		ips, err := resolver.LookupHost(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("netguard: resolve %q: %w", host, err)
		}

		// Issue 1 (TOCTOU / DNS rebinding): check ALL resolved IPs before
		// dialling. If ANY IP fails, reject the entire dial — an upstream
		// that resolves to both a public and a private IP is almost certainly
		// misconfigured or malicious. Do NOT dial only the "safe" subset.
		for _, ip := range ips {
			if err := BlockPrivate(ip, allowPrivate); err != nil {
				return nil, fmt.Errorf("netguard: dial blocked: %w", err)
			}
		}

		// Dial by IP, not by hostname, so the IP actually connected to is
		// the IP that was checked. This eliminates the TOCTOU window where a
		// second DNS resolution (inside baseDialer.DialContext) could return a
		// different IP than the one we validated. TLS SNI is unaffected because
		// http.Transport sets tls.Config.ServerName from the URL host, not the
		// dialled address.
		//
		// Policy: dial the first IP that passed BlockPrivate (i.e. the first
		// element of ips, since we returned on any failure above).
		dialAddr := net.JoinHostPort(ips[0], port)
		return baseDialer.DialContext(ctx, network, dialAddr)
	}
}
