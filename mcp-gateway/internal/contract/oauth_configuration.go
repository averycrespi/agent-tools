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

func ParseOAuthCallbackURI(raw string) (*url.URL, string, error) {
	maximum, _ := FixedLimitByName("oauth_url_bytes")
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" || int64(len(raw)) > maximum.Maximum || parsed.Scheme != "http" || parsed.Opaque != "" || parsed.User != nil || strings.ContainsAny(raw, "?#%\\") || parsed.String() != raw || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || path.Clean(parsed.Path) != parsed.Path {
		return nil, "", ErrOAuthConfiguration
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
