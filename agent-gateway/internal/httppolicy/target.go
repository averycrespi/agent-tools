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

// ParseConnect requires explicit host:port and agreement with the supplied
// effective authority. The outer HTTP parser gives the CONNECT target precedence
// over raw Host. This parser confers neither tunnel nor request permission.
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
	escapedPath                 string
	forceQuery                  bool
}

func (r Request) Destination() Destination { return r.destination }
func (r Request) Scheme() string           { return r.scheme }
func (r Request) Method() string           { return r.method }
func (r Request) Path() string             { return r.path }

// URL returns a fresh forwarding value, distinct from the comparison Path.
// RawPath preserves accepted escape spelling; queries are never selectors.
func (r Request) URL() *url.URL {
	decoded, _ := url.PathUnescape(r.escapedPath)
	return &url.URL{Scheme: r.scheme, Host: r.destination.Authority(), Path: decoded, RawPath: r.escapedPath, RawQuery: r.query, ForceQuery: r.forceQuery}
}

// ParseRequest parses an absolute URI with the effective Host. Outer absolute
// HTTP uses target authority; intercepted origin requests use inner Host/:authority.
// A nonnil CONNECT
// destination binds HTTPS to its intercepted connection. SNI, when present,
// must agree as well. Adapters must reject duplicate Host fields beforehand.
func ParseRequest(raw, method, hostHeader, sni string, connect *Destination) (Request, error) {
	if len(raw) > contract.HTTPTargetBytes {
		return Request{}, targetError("target_too_long")
	}
	if len(raw) == 0 || !utf8.ValidString(raw) || !validMethod(method) || method == "CONNECT" {
		return Request{}, targetError("invalid_target_syntax")
	}
	for _, b := range []byte(raw) {
		if b < 0x21 || b == 0x7f || strings.ContainsRune("\\#\"<>^`{|}", rune(b)) {
			return Request{}, targetError("invalid_target_syntax")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Request{}, targetError("invalid_target_syntax")
	}
	port := uint16(80)
	if u.Scheme == "https" {
		port = 443
	}
	d, err := parseAuthority(u.Host, port)
	h, e := parseAuthority(hostHeader, port)
	if err != nil || e != nil || d != h {
		return Request{}, targetError("authority_mismatch")
	}
	if connect != nil && (u.Scheme != "https" || *connect != d) {
		return Request{}, targetError("authority_mismatch")
	}
	if sni != "" {
		sh, e := canonicalHost(sni)
		if e != nil || sh != d.host || u.Scheme != "https" {
			return Request{}, targetError("authority_mismatch")
		}
	}
	// Validate input syntax before net/url can escape unsupported ASCII bytes.
	pathInput := raw[strings.Index(raw, "://")+3+len(u.Host):]
	pathInput, _, _ = strings.Cut(pathInput, "?")
	if !uriComponent(pathInput, false) || !uriComponent(u.RawQuery, true) {
		return Request{}, targetError("invalid_target_syntax")
	}
	escaped := encodeUnicode(pathInput)
	if escaped == "" {
		escaped = "/"
	}
	p, err := requestPath(escaped)
	if err != nil {
		return Request{}, err
	}
	// Queries remain opaque, but malformed escaping/control bytes cannot pass
	// through a second parser with a different interpretation.
	if _, err = url.QueryUnescape(u.RawQuery); err != nil {
		return Request{}, targetError("invalid_target_syntax")
	}
	query := encodeUnicode(u.RawQuery)
	result := Request{destination: d, scheme: u.Scheme, method: method, path: p, escapedPath: escaped, query: query, forceQuery: u.ForceQuery}
	if len(result.URL().String()) > contract.HTTPTargetBytes {
		return Request{}, targetError("target_too_long")
	}
	return result, nil
}

// canonicalPath is the unchanged v1 selector compiler, not a request parser.
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
