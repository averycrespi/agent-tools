// Package controlclient owns strict local public-control HTTP transport and admin bearer acquisition.
package controlclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

const (
	DefaultAddress    = "http://127.0.0.1:8210"
	MaxResponseBytes  = 1 * 1024 * 1024
	MaxJSONDepth      = 64
	MaxErrorTextBytes = 128

	defaultConnectTimeout = 5 * time.Second
	defaultHeaderTimeout  = 15 * time.Second
	defaultRequestTimeout = 30 * time.Second
	maxResponseHeader     = 64 * 1024
)

var (
	ErrInvalidAddress  = errors.New("control address is invalid")
	ErrInvalidRequest  = errors.New("control request is invalid")
	ErrTransport       = errors.New("control transport failed")
	ErrRedirect        = errors.New("control redirect is not allowed")
	ErrResponseInvalid = errors.New("control response is invalid")
)

var addressPattern = regexp.MustCompile(`^http://127\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2}):([1-9][0-9]{0,4})$`)

type Handoff uint8

const (
	HandoffNone Handoff = iota
	HandoffPossible
)

type Failure struct {
	kind    error
	handoff Handoff
	refused bool
}

func (failure *Failure) Error() string {
	if errors.Is(failure.kind, ErrInvalidAddress) {
		return "control address is invalid"
	}
	if errors.Is(failure.kind, ErrInvalidRequest) {
		return "control request is invalid"
	}
	if errors.Is(failure.kind, ErrRedirect) {
		return "control redirect is not allowed"
	}
	if errors.Is(failure.kind, ErrResponseInvalid) {
		return "control response is invalid"
	}
	return "control transport failed"
}

func (failure *Failure) Unwrap() error { return failure.kind }

func FailureHandoff(err error) Handoff {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.handoff
	}
	return HandoffNone
}

func FailureRefused(err error) bool {
	var failure *Failure
	return errors.As(err, &failure) && failure.refused
}

type TransportOptions struct {
	DialContext    func(context.Context, string, string) (net.Conn, error)
	ConnectTimeout time.Duration
	HeaderTimeout  time.Duration
	RequestTimeout time.Duration
}

type Client struct {
	address        string
	http           *http.Client
	requestTimeout time.Duration
}

type Request struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func New(address string, options TransportOptions) (*Client, error) {
	if err := validateAddress(address); err != nil {
		return nil, &Failure{kind: ErrInvalidAddress}
	}
	connectTimeout := positiveOr(options.ConnectTimeout, defaultConnectTimeout)
	headerTimeout := positiveOr(options.HeaderTimeout, defaultHeaderTimeout)
	requestTimeout := positiveOr(options.RequestTimeout, defaultRequestTimeout)
	dial := options.DialContext
	if dial == nil {
		dialer := &net.Dialer{Timeout: connectTimeout}
		dial = dialer.DialContext
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dial,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		ResponseHeaderTimeout:  headerTimeout,
		MaxResponseHeaderBytes: maxResponseHeader,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirect
		},
	}
	return &Client{address: address, http: client, requestTimeout: requestTimeout}, nil
}

func (client *Client) Address() string { return client.address }

func (client *Client) Do(ctx context.Context, request Request) (Response, error) {
	if client == nil || client.http == nil || request.Method == "" || !validRequestPath(request.Path) || len(request.Body) > MaxResponseBytes || !validRequestHeaders(request.Header) {
		return Response{}, &Failure{kind: ErrInvalidRequest}
	}
	requestContext, cancel := context.WithTimeout(ctx, client.requestTimeout)
	defer cancel()
	var handedOff atomic.Bool
	trace := &httptrace.ClientTrace{WroteHeaders: func() { handedOff.Store(true) }}
	requestContext = httptrace.WithClientTrace(requestContext, trace)
	httpRequest, err := http.NewRequestWithContext(requestContext, request.Method, client.address+request.Path, bytes.NewReader(request.Body))
	if err != nil {
		return Response{}, &Failure{kind: ErrInvalidRequest}
	}
	httpRequest.Header = cloneHeader(request.Header)
	response, err := client.http.Do(httpRequest)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		kind := ErrTransport
		if errors.Is(err, ErrRedirect) {
			kind = ErrRedirect
		}
		handoff := handoffValue(handedOff.Load())
		return Response{}, &Failure{kind: kind, handoff: handoff, refused: errors.Is(kind, ErrTransport) && handoff == HandoffNone && errors.Is(err, syscall.ECONNREFUSED)}
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil || len(body) > MaxResponseBytes {
		return Response{}, &Failure{kind: ErrResponseInvalid, handoff: HandoffPossible}
	}
	if len(body) > 0 {
		if _, err := strictjson.ParseValue(body, strictjson.Options{MaxBytes: MaxResponseBytes, MaxDepth: MaxJSONDepth}); err != nil {
			return Response{}, &Failure{kind: ErrResponseInvalid, handoff: HandoffPossible}
		}
	}
	return Response{StatusCode: response.StatusCode, Header: cloneHeader(response.Header), Body: body}, nil
}

func ListenAuthority(address string) (string, error) {
	if err := validateAddress(address); err != nil {
		return "", err
	}
	return strings.TrimPrefix(address, "http://"), nil
}

func validateAddress(address string) error {
	if strings.TrimSpace(address) != address || strings.Contains(address, "%") {
		return ErrInvalidAddress
	}
	matches := addressPattern.FindStringSubmatch(address)
	if len(matches) != 5 {
		return ErrInvalidAddress
	}
	for _, raw := range matches[1:4] {
		octet, err := strconv.Atoi(raw)
		if err != nil || octet > 255 {
			return ErrInvalidAddress
		}
	}
	port, err := strconv.Atoi(matches[4])
	if err != nil || port < 1 || port > 65535 {
		return ErrInvalidAddress
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.String() != address || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrInvalidAddress
	}
	return nil
}

func validRequestPath(path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "#") || strings.ContainsAny(path, "\r\n") {
		return false
	}
	parsed, err := url.ParseRequestURI(path)
	return err == nil && !parsed.IsAbs() && parsed.Host == ""
}

func validRequestHeaders(header http.Header) bool {
	for name := range header {
		switch http.CanonicalHeaderKey(name) {
		case "Cookie", "Proxy-Authorization", "Proxy-Connection":
			return false
		}
	}
	return true
}

func positiveOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func handoffValue(handedOff bool) Handoff {
	if handedOff {
		return HandoffPossible
	}
	return HandoffNone
}

func cloneHeader(source http.Header) http.Header {
	if source == nil {
		return make(http.Header)
	}
	return source.Clone()
}
