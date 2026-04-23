package server

import (
	"errors"
	"fmt"
	"net"
)

// ValidateLoopbackAddr refuses to bind anything but a loopback interface.
// The broker proxies tools holding real secrets (OAuth tokens, API keys)
// and protects them with only a bearer token over plain HTTP. That posture
// assumes the listener is not network-reachable. README and DESIGN spell
// this out in prose; this is the load-bearing enforcement.
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
			"(the broker is protected only by a bearer token — see README security notes)", addr)
	}
	return nil
}
