package contract

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
)

var ErrOAuthConfiguration = errors.New("OAuth configuration is invalid")

// ParseOAuthCallbackURI validates syntax only. HTTPS callbacks have no bind
// address and must additionally match the configured main callback exactly.
func ParseOAuthCallbackURI(raw string) (*url.URL, string, error) {
	maximum, _ := FixedLimitByName("oauth_url_bytes")
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" || int64(len(raw)) > maximum.Maximum || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Opaque != "" || parsed.User != nil || strings.ContainsAny(raw, "?#%\\") || parsed.String() != raw || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || path.Clean(parsed.Path) != parsed.Path {
		return nil, "", ErrOAuthConfiguration
	}
	if parsed.Scheme == "https" {
		if parsed.Path != "/oauth/callback" || !validHTTPSCallbackAuthority(parsed) {
			return nil, "", ErrOAuthConfiguration
		}
		return parsed, "", nil
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil || port == 0 || strconv.FormatUint(port, 10) != parsed.Port() {
		return nil, "", ErrOAuthConfiguration
	}
	host := parsed.Hostname()
	bind := host
	if host == "localhost" {
		bind = "127.0.0.1"
	} else {
		address, err := netip.ParseAddr(host)
		if err != nil || !address.IsLoopback() || address.Zone() != "" || address.Is4In6() || address.String() != host {
			return nil, "", ErrOAuthConfiguration
		}
	}
	if net.JoinHostPort(host, parsed.Port()) != parsed.Host {
		return nil, "", ErrOAuthConfiguration
	}
	return parsed, net.JoinHostPort(bind, parsed.Port()), nil
}

func validHTTPSCallbackAuthority(parsed *url.URL) bool {
	host := parsed.Hostname()
	canonical, ok := NormalizeHostname(host)
	if address, err := netip.ParseAddr(host); err == nil && address.Zone() == "" && !address.IsUnspecified() && !address.IsMulticast() && address.String() == host {
		canonical, ok = address.String(), true
		if address.Is6() {
			canonical = "[" + canonical + "]"
		}
	} else if canonical != host {
		ok = false
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 || value == 443 || strconv.FormatUint(value, 10) != port {
			return false
		}
		canonical += ":" + port
	}
	return ok && parsed.Host == canonical
}

// ValidOAuthCallbackSelection binds HTTPS to configured authority, never to
// request headers. Local mode retains its callback-only loopback listeners.
func ValidOAuthCallbackSelection(main, selected string) bool {
	parsed, _, err := ParseOAuthCallbackURI(selected)
	if err != nil {
		return false
	}
	if strings.HasPrefix(main, "https://") || parsed.Scheme == "https" {
		return selected == main
	}
	return true
}

func NormalizeOAuthScopes(values []string) ([]string, error) {
	count, _ := FixedLimitByName("oauth_scope_count")
	tokenBytes, _ := FixedLimitByName("oauth_scope_token_bytes")
	totalBytes, _ := FixedLimitByName("oauth_scope_bytes")
	if int64(len(values)) > count.Maximum {
		return nil, ErrOAuthConfiguration
	}
	result := slices.Clone(values)
	if result == nil {
		result = []string{}
	}
	for _, value := range result {
		if value == "" || int64(len(value)) > tokenBytes.Maximum {
			return nil, ErrOAuthConfiguration
		}
		for _, b := range []byte(value) {
			if b != 0x21 && (b < 0x23 || b > 0x5b) && (b < 0x5d || b > 0x7e) {
				return nil, ErrOAuthConfiguration
			}
		}
	}
	slices.Sort(result)
	result = slices.Compact(result)
	if int64(len(strings.Join(result, " "))) > totalBytes.Maximum {
		return nil, ErrOAuthConfiguration
	}
	return result, nil
}
