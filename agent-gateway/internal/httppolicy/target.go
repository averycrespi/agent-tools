// Package httppolicy implements immutable HTTP v1 policy, without I/O or identity authority.
package httppolicy

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

var ErrInvalid = errors.New("invalid HTTP policy or coordinates")

// Destination can only be constructed by the canonical parsers. Its zero value
// is invalid. Forwarders must use these coordinates, never the original input.
type Destination struct {
	host string
	port uint16
}

func (d Destination) Host() string      { return d.host }
func (d Destination) Port() uint16      { return d.port }
func (d Destination) Authority() string { return net.JoinHostPort(d.host, strconv.Itoa(int(d.port))) }

func NewDestination(host string, port uint16) (Destination, error) {
	h, err := canonicalHost(host)
	if err != nil || port == 0 {
		return Destination{}, ErrInvalid
	}
	return Destination{h, port}, nil
}

func canonicalHost(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > contract.HTTPHostBytes || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\\/%@[]?# \t\r\n") || strings.HasSuffix(raw, ".") {
		return "", ErrInvalid
	}
	if ip, err := netip.ParseAddr(raw); err == nil {
		if ip.Zone() != "" {
			return "", ErrInvalid
		}
		return ip.Unmap().String(), nil
	}
	// IDNA lookup mapping plus strict DNS validation; alternate numeric IP
	// forms are never delegated to resolver-specific interpretation.
	h, err := idna.Lookup.ToASCII(raw)
	if err != nil || len(h) > contract.HTTPHostBytes {
		return "", ErrInvalid
	}
	h = strings.ToLower(h)
	h, ok := contract.NormalizeHostname(h)
	if !ok {
		return "", ErrInvalid
	}
	labels := strings.Split(h, ".")
	last := labels[len(labels)-1]
	if strings.HasPrefix(last, "0x") && strings.Trim(last[2:], "0123456789abcdef") == "" {
		return "", ErrInvalid
	}
	return h, nil
}

func parseAuthority(raw string, defaultPort uint16) (Destination, error) {
	if raw == "" || len(raw) > contract.HTTPHostBytes+8 {
		return Destination{}, ErrInvalid
	}
	host, port := raw, defaultPort
	if strings.HasPrefix(raw, "[") || strings.Contains(raw, ":") {
		h, p, err := net.SplitHostPort(raw)
		if err != nil {
			// An IPv6 URL authority may omit its default port, but CONNECT may not.
			if defaultPort == 0 || !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
				return Destination{}, ErrInvalid
			}
			h = raw[1 : len(raw)-1]
			if ip, e := netip.ParseAddr(h); e != nil || !ip.Is6() {
				return Destination{}, ErrInvalid
			}
		} else {
			n, e := strconv.ParseUint(p, 10, 16)
			if e != nil || n == 0 || strconv.FormatUint(n, 10) != p {
				return Destination{}, ErrInvalid
			}
			port = uint16(n)
		}
		host = h
	}
	return NewDestination(host, port)
}

// ParseConnect requires explicit host:port and agreement with the mandatory
// Host authority. It confers neither tunnel nor request permission.
func ParseConnect(authority, hostHeader string) (Destination, error) {
	d, err := parseAuthority(authority, 0)
	h, e := parseAuthority(hostHeader, 0)
	if err != nil || e != nil || d != h {
		return Destination{}, ErrInvalid
	}
	return d, nil
}

type Request struct {
	destination                 Destination
	scheme, method, path, query string
	forceQuery                  bool
}

func (r Request) Destination() Destination { return r.destination }
func (r Request) Scheme() string           { return r.scheme }
func (r Request) Method() string           { return r.method }
func (r Request) Path() string             { return r.path }

// URL returns a fresh value with the exact canonical policy path. Query is
// preserved for forwarding, never interpreted as a policy selector/evidence.
func (r Request) URL() *url.URL {
	return &url.URL{Scheme: r.scheme, Host: r.destination.Authority(), Path: r.path, RawQuery: r.query, ForceQuery: r.forceQuery}
}

// ParseRequest parses an absolute URI with mandatory Host. A nonnil CONNECT
// destination binds HTTPS to its intercepted connection. SNI, when present,
// must agree as well. Adapters must reject duplicate Host fields beforehand.
func ParseRequest(raw, method, hostHeader, sni string, connect *Destination) (Request, error) {
	if len(raw) == 0 || len(raw) > contract.HTTPTargetBytes || !validMethod(method) || method == "CONNECT" || strings.ContainsAny(raw, "\r\n\t\\#") {
		return Request{}, ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Request{}, ErrInvalid
	}
	port := uint16(80)
	if u.Scheme == "https" {
		port = 443
	}
	d, err := parseAuthority(u.Host, port)
	h, e := parseAuthority(hostHeader, port)
	if err != nil || e != nil || d != h {
		return Request{}, ErrInvalid
	}
	if connect != nil && (u.Scheme != "https" || *connect != d) {
		return Request{}, ErrInvalid
	}
	if sni != "" {
		sh, e := canonicalHost(sni)
		if e != nil || sh != d.host || u.Scheme != "https" {
			return Request{}, ErrInvalid
		}
	}
	p, err := canonicalPath(u.EscapedPath())
	if err != nil {
		return Request{}, err
	}
	// Queries remain opaque, but malformed escaping/control bytes cannot pass
	// through a second parser with a different interpretation.
	if _, err = url.QueryUnescape(u.RawQuery); err != nil {
		return Request{}, ErrInvalid
	}
	for _, b := range []byte(u.RawQuery) {
		if b < 0x21 || b > 0x7e {
			return Request{}, ErrInvalid
		}
	}
	result := Request{d, u.Scheme, method, p, u.RawQuery, u.ForceQuery}
	if len(result.URL().String()) > contract.HTTPTargetBytes {
		return Request{}, ErrInvalid
	}
	return result, nil
}

func canonicalPath(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	if len(raw) > contract.HTTPPathBytes || raw[0] != '/' {
		return "", ErrInvalid
	}
	// Only unreserved escapes are decoded. Encoded separators, percent (double
	// decoding), controls and reserved delimiters are unsafe, not alternate paths.
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b == '%' {
			if i+2 >= len(raw) {
				return "", ErrInvalid
			}
			n, e := strconv.ParseUint(raw[i+1:i+3], 16, 8)
			if e != nil || !unreserved(byte(n)) {
				return "", ErrInvalid
			}
			b = byte(n)
			i += 2
		}
		// A literal @ is path data, not userinfo (which is rejected in the
		// authority). Keep escaped reserved bytes rejected: decoding those
		// could change the resource understood by an upstream router.
		if !unreserved(b) && b != '/' && b != '@' {
			return "", ErrInvalid
		}
		out.WriteByte(b)
	}
	p := out.String()
	if strings.Contains(p, "//") {
		return "", ErrInvalid
	}
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." {
			return "", ErrInvalid
		}
	}
	return p, nil
}
func unreserved(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~", rune(b))
}
func validMethod(s string) bool {
	if len(s) == 0 || len(s) > contract.HTTPMethodBytes {
		return false
	}
	for _, b := range []byte(s) {
		if (b < 'A' || b > 'Z') && (b < '0' || b > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b)) {
			return false
		}
	}
	return true
}
