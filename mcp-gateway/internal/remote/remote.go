// Package remote owns hardened outbound HTTP construction and address policy.
package remote

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

var (
	ErrInvalidURL    = errors.New("remote URL is invalid")
	ErrAddressPolicy = errors.New("remote address is not allowed")
	ErrResponseLimit = errors.New("remote response exceeds a limit")
	ErrRedirect      = errors.New("remote redirect is not allowed")
)

type Policy struct {
	AllowLoopbackHTTP bool
	AllowRestricted   bool
}

type Endpoint struct {
	url             *url.URL
	allowRestricted bool
}

func (endpoint Endpoint) String() string { return endpoint.url.String() }

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Options struct {
	Resolver    Resolver
	DialContext func(context.Context, string, string) (net.Conn, error)
}

type Factory struct {
	resolver Resolver
	dial     func(context.Context, string, string) (net.Conn, error)
}

type Request struct {
	Endpoint        Endpoint
	Method          string
	Header          http.Header
	Body            []byte
	MaxBody         int64
	BeforeRoundTrip func()
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type OpenResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
	maxBody    int64
}

func ParseEndpoint(raw string, allowLoopbackHTTP bool) (Endpoint, error) {
	return Parse(raw, Policy{AllowLoopbackHTTP: allowLoopbackHTTP})
}

func Parse(raw string, policy Policy) (Endpoint, error) {
	limit, _ := contract.FixedLimitByName("resource_url_bytes")
	if raw == "" || int64(len(raw)) > limit.Maximum {
		return Endpoint{}, ErrInvalidURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" || parsed.Path == "" || parsed.Path[0] != '/' {
		return Endpoint{}, ErrInvalidURL
	}
	if !validHost(parsed.Hostname()) {
		return Endpoint{}, ErrInvalidURL
	}
	if parsed.Scheme != "https" {
		address, addressErr := netip.ParseAddr(parsed.Hostname())
		if parsed.Scheme != "http" || !policy.AllowLoopbackHTTP || addressErr != nil || !address.IsLoopback() {
			return Endpoint{}, ErrInvalidURL
		}
	}
	if parsed.Port() != "" {
		port, portErr := strconv.ParseUint(parsed.Port(), 10, 16)
		if portErr != nil || port == 0 {
			return Endpoint{}, ErrInvalidURL
		}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = canonicalHost(parsed)
	return Endpoint{url: parsed, allowRestricted: policy.AllowRestricted || (policy.AllowLoopbackHTTP && parsed.Scheme == "http")}, nil
}

func canonicalHost(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	if address, err := netip.ParseAddr(host); err == nil {
		host = address.String()
	}
	port := parsed.Port()
	if port == "" || (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, port)
}

func New(options Options) *Factory {
	if options.Resolver == nil {
		options.Resolver = net.DefaultResolver
	}
	if options.DialContext == nil {
		dialer := &net.Dialer{Timeout: contract.DownstreamConnectDeadline}
		options.DialContext = dialer.DialContext
	}
	return &Factory{resolver: options.Resolver, dial: options.DialContext}
}

func (factory *Factory) Exchange(ctx context.Context, request Request) (Response, error) {
	response, err := factory.Open(ctx, request)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, response.maxBody+1))
	if err != nil {
		return Response{}, fmt.Errorf("read remote response: %w", err)
	}
	if int64(len(body)) > response.maxBody {
		return Response{}, ErrResponseLimit
	}
	return Response{StatusCode: response.StatusCode, Header: response.Header, Body: body}, nil
}

func (factory *Factory) Open(ctx context.Context, request Request) (*OpenResponse, error) {
	if request.Endpoint.url == nil || request.Method == "" || request.MaxBody < 1 {
		return nil, ErrInvalidURL
	}
	bodyLimit, _ := contract.FixedLimitByName("downstream_mcp_body_bytes")
	if int64(len(request.Body)) > bodyLimit.Maximum {
		return nil, ErrResponseLimit
	}
	if err := validateHeaders(request.Header); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		TLSHandshakeTimeout:   contract.DownstreamConnectDeadline,
		ResponseHeaderTimeout: contract.MaximumDownstreamCallDeadline,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return factory.dialPinned(ctx, network, request.Endpoint)
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: request.Endpoint.url.Hostname()}, //nolint:gosec // platform roots and hostname verification remain enabled.
	}
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect },
	}
	requestCtx, cancel := context.WithCancel(ctx)
	httpRequest, err := http.NewRequestWithContext(requestCtx, request.Method, request.Endpoint.String(), bytes.NewReader(request.Body))
	if err != nil {
		cancel()
		transport.CloseIdleConnections()
		return nil, ErrInvalidURL
	}
	httpRequest.Header = cloneHeader(request.Header)
	if err := requestCtx.Err(); err != nil {
		cancel()
		transport.CloseIdleConnections()
		return nil, err
	}
	if request.BeforeRoundTrip != nil {
		request.BeforeRoundTrip()
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		cancel()
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("remote exchange: %w", err)
	}
	if err := validateHeaders(response.Header); err != nil {
		cancel()
		_ = response.Body.Close()
		transport.CloseIdleConnections()
		return nil, err
	}
	return &OpenResponse{StatusCode: response.StatusCode, Header: cloneHeader(response.Header), Body: &responseBody{ReadCloser: response.Body, transport: transport, cancel: cancel}, maxBody: request.MaxBody}, nil
}

type responseBody struct {
	io.ReadCloser
	transport *http.Transport
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

func (body *responseBody) Close() error {
	body.closeOnce.Do(func() {
		body.cancel()
		body.closeErr = body.ReadCloser.Close()
		body.transport.CloseIdleConnections()
	})
	return body.closeErr
}

func (factory *Factory) dialPinned(ctx context.Context, network string, endpoint Endpoint) (net.Conn, error) {
	host := endpoint.url.Hostname()
	port := endpoint.url.Port()
	if port == "" {
		if endpoint.url.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	addresses, err := factory.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, ErrAddressPolicy
	}
	for _, address := range addresses {
		if !allowedAddress(address, endpoint.allowRestricted) {
			return nil, ErrAddressPolicy
		}
	}
	return factory.dial(ctx, network, net.JoinHostPort(addresses[0].String(), port))
}

func allowedAddress(address netip.Addr, allowRestricted bool) bool {
	address = address.Unmap()
	if allowRestricted && address.IsValid() && (address.IsGlobalUnicast() || address.IsLoopback()) && !address.IsUnspecified() && !address.IsMulticast() {
		return true
	}
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func validHost(host string) bool {
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Zone() == ""
	}
	if host == "" || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("5f00::/16"),
}

func validateHeaders(header http.Header) error {
	countLimit, _ := contract.FixedLimitByName("request_header_count")
	bytesLimit, _ := contract.FixedLimitByName("request_header_bytes")
	valueLimit, _ := contract.FixedLimitByName("request_header_value_bytes")
	var count, size int64
	for name, values := range header {
		if !validHeaderName(name) {
			return ErrResponseLimit
		}
		count += int64(len(values))
		for _, value := range values {
			if !validHeaderValue(value) || int64(len(value)) > valueLimit.Maximum {
				return ErrResponseLimit
			}
			size += int64(len(name) + len(value))
		}
	}
	if count > countLimit.Maximum || size > bytesLimit.Maximum {
		return ErrResponseLimit
	}
	return nil
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for _, character := range []byte(value) {
		if character == '\t' || character >= ' ' && character != 0x7f {
			continue
		}
		return false
	}
	return true
}

func cloneHeader(source http.Header) http.Header {
	result := make(http.Header, len(source))
	for name, values := range source {
		result[name] = append([]string(nil), values...)
	}
	return result
}
