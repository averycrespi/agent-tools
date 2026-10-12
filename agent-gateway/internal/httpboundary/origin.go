package httpboundary

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// BrowserOrigin separates public browser authority from the transport bind.
// Forwarding headers never participate in this configuration.
func BrowserOrigin(authority, external string) (string, error) {
	if err := ValidateAuthority(authority); err != nil {
		return "", err
	}
	host, _, _ := net.SplitHostPort(authority)
	if external == "" {
		if !net.ParseIP(host).IsLoopback() {
			return "", fmt.Errorf("non-loopback --listen requires --external-origin with a canonical HTTPS origin")
		}
		return "http://" + authority, nil
	}
	u, err := url.Parse(external)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return "", fmt.Errorf("--external-origin must be a canonical HTTPS origin without credentials, path, query or fragment")
	}
	hostname, ok := contract.NormalizeHostname(u.Hostname())
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast() && ip.String() == u.Hostname() {
		hostname, ok = ip.String(), true
		if ip.Is6() {
			hostname = "[" + hostname + "]"
		}
	} else if hostname != u.Hostname() {
		ok = false
	}
	if !ok {
		return "", fmt.Errorf("--external-origin requires a canonical IP address or lowercase ASCII DNS hostname")
	}
	canonical := "https://" + hostname
	if port := u.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 || strconv.FormatUint(value, 10) != port || value == 443 {
			return "", fmt.Errorf("--external-origin port must be canonical and nonzero; omit the default HTTPS port 443")
		}
		canonical += ":" + port
	}
	if external != canonical {
		return "", fmt.Errorf("--external-origin must be canonical (no trailing delimiters)")
	}
	return canonical, nil
}

// LocalAuthority is the explicit loopback endpoint for local CLI/probes and
// local OAuth callbacks, not a browser origin for a remotely served Gateway.
func LocalAuthority(authority string) string {
	host, port, _ := net.SplitHostPort(authority)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return authority
	}
	return net.JoinHostPort("127.0.0.1", port)
}
