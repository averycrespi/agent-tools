package server

import (
	"errors"
	"fmt"
	"net"
)

// ValidateLoopbackAddr refuses to bind anything but a loopback interface.
// The proxy is unauthenticated and its whole security posture relies on not
// being network-reachable. README and DESIGN spell this out in prose; this
// is the load-bearing enforcement.
func ValidateLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parsing --addr: %w", err)
	}
	if host == "" {
		return errors.New("--addr has no host — would bind all interfaces; use 127.0.0.1 or localhost")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("--addr host %q is not an IP or 'localhost'; refusing to bind", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("--addr %q is not a loopback interface; refusing to bind "+
			"(the proxy is unauthenticated — see README security notes)", addr)
	}
	return nil
}
