package config

import (
	"errors"
	"fmt"
	"net"
)

// ValidateLoopbackAddr refuses to bind anything but a loopback interface.
// Loopback-only binding is the defense-in-depth boundary that keeps these
// listeners off the LAN — load-bearing because the admin token, per-agent
// tokens, and cached secret ciphertexts are all reachable through these
// endpoints. README and DESIGN spell this out in prose; this is the
// programmatic enforcement.
//
// Ported from mcp-broker/internal/server/addr.go; keep behavior in sync.
func ValidateLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parsing listen addr: %w", err)
	}
	if host == "" {
		return errors.New("listen addr has no host — would bind all interfaces; use 127.0.0.1 or localhost")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("listen addr host %q is not an IP or 'localhost'; refusing to bind", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("listen addr %q is not a loopback interface; refusing to bind "+
			"(listeners expose host-local tokens and cached secrets — see README security notes)", addr)
	}
	return nil
}
