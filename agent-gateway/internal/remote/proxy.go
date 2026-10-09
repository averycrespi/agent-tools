package remote

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

// ProxyAddress is a single-use resolved destination, never a pooled permission.
// Policy evaluates Facts before receipt confirmation. Dial rechecks exclusions
// against the current composition-owned listener inventory without resolving.
type ProxyAddress struct {
	factory     *Factory
	destination httppolicy.Destination
	facts       httppolicy.AddressFacts
	listeners   func() []netip.AddrPort
	used        atomic.Bool
}

func (f *Factory) ResolveProxy(ctx context.Context, d httppolicy.Destination, listeners func() []netip.AddrPort) (*ProxyAddress, error) {
	if d.Host() == "" || d.Port() == 0 || listeners == nil {
		return nil, ErrAddressPolicy
	}
	ctx, cancel := context.WithTimeout(ctx, contract.HTTPProxyDialTimeout)
	defer cancel()
	var addresses []netip.Addr
	if ip, err := netip.ParseAddr(d.Host()); err == nil {
		addresses = []netip.Addr{ip}
	} else {
		var err error
		addresses, err = f.resolver.LookupNetIP(ctx, "ip", d.Host())
		if err != nil {
			return nil, proxyTransportFailure(err)
		}
	}
	if len(addresses) == 0 || len(addresses) > contract.HTTPAddressFacts {
		return nil, ErrProxyConnection
	}
	owned := listeners()
	if len(owned) > contract.HTTPAddressFacts {
		return nil, ErrAddressPolicy
	}
	return &ProxyAddress{factory: f, destination: d, listeners: listeners, facts: httppolicy.AddressFacts{Complete: true, Addresses: append([]netip.Addr(nil), addresses...), GatewayListeners: append([]netip.AddrPort(nil), owned...)}}, nil
}

func (p *ProxyAddress) Facts() httppolicy.AddressFacts {
	return httppolicy.AddressFacts{Complete: p.facts.Complete, Addresses: append([]netip.Addr(nil), p.facts.Addresses...), GatewayListeners: append([]netip.AddrPort(nil), p.facts.GatewayListeners...)}
}

func (p *ProxyAddress) Dial(ctx context.Context, private bool) (net.Conn, error) {
	if p == nil || !p.used.CompareAndSwap(false, true) {
		return nil, ErrAddressPolicy
	}
	return p.dialCandidates(ctx, private)
}

func (p *ProxyAddress) validateDial(private bool) error {
	owned := p.listeners()
	if len(owned) > contract.HTTPAddressFacts {
		return ErrAddressPolicy
	}
	for _, listener := range owned {
		if !listener.IsValid() || listener.Port() == 0 || listener.Addr().Zone() != "" {
			return ErrAddressPolicy
		}
	}
	for _, ip := range p.facts.Addresses {
		class := httppolicy.ClassifyAddress(ip)
		if class == httppolicy.AddressForbidden || class == httppolicy.AddressPrivate && !private {
			return ErrAddressPolicy
		}
		for _, listener := range owned {
			if ip.Unmap() == listener.Addr().Unmap() && p.destination.Port() == listener.Port() {
				return ErrAddressPolicy
			}
		}
	}
	return nil
}

func (p *ProxyAddress) dialCandidates(ctx context.Context, private bool) (result net.Conn, resultErr error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, contract.HTTPProxyDialTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	attempted, timeoutOrdinal := 0, 0
	lastFamily, stopReason, contextState := "none", "exhausted", "none"
	var candidateBudget time.Duration
	var lastNative, timeoutNative string
	defer func() {
		if resultErr == nil {
			return
		}
		facts := fmt.Sprintf("attempted=%d available=%d last_family=%s candidate_budget_ms=%d total_budget_ms=%d elapsed_ms=%d stop=%s context=%s omitted_samples=%d; timeout_candidate=%d timeout_cause=%s; last_native=%s", attempted, len(p.facts.Addresses), lastFamily, max(candidateBudget.Milliseconds(), 0), max(deadline.Sub(started).Milliseconds(), 0), max(time.Since(started).Milliseconds(), 0), stopReason, contextState, max(attempted-2, 0), timeoutOrdinal, timeoutNative, lastNative)
		resultErr = diagnostics.WithDetail(resultErr, diagnostics.Detail{Component: "remote", Operation: "dial candidates", Resource: diagnostics.Text(p.destination.Authority(), 160), Explanation: facts, Effect: "application_dispatch=not_started"})
	}()
	failure := ErrProxyConnection
	for i, ip := range p.facts.Addresses {
		if err := p.validateDial(private); err != nil {
			stopReason = "address_policy"
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			stopReason = "total_or_parent_context"
			contextState = diagnostics.Text(err.Error(), 64)
			return nil, proxyTransportFailure(err)
		}
		// Share the remaining budget rather than letting a stalled first address
		// consume it all. There is one synchronous dial owner, never a race to
		// send application bytes. Every subdeadline is bounded by the same total.
		now := time.Now()
		attemptDeadline := now.Add(deadline.Sub(now) / time.Duration(len(p.facts.Addresses)-i))
		candidateBudget = attemptDeadline.Sub(now)
		attempted++
		lastFamily = "ipv6"
		if ip.Is4() || ip.Is4In6() {
			lastFamily = "ipv4"
		}
		attempt, stop := context.WithDeadline(ctx, attemptDeadline)
		conn, err := p.factory.dial(attempt, "tcp", netip.AddrPortFrom(ip, p.destination.Port()).String())
		attemptErr := attempt.Err()
		if attemptErr == nil && !time.Now().Before(attemptDeadline) {
			attemptErr = context.DeadlineExceeded
		}
		stop()
		if err == nil && attemptErr == nil && conn != nil {
			return &proxyIdleConn{Conn: conn}, nil
		}
		if conn != nil {
			_ = conn.Close()
		}
		lastNative = diagnostics.Text(diagnostics.Snapshot("remote", "dial", p.destination.Authority(), err).Explanation, 96)
		if err == nil {
			lastNative = "dial returned no usable connection"
		}
		contextState = "none"
		if attemptErr != nil {
			contextState = diagnostics.Text(attemptErr.Error(), 64)
			err = attemptErr
		}
		category := ErrProxyConnection
		if errors.Is(proxyTransportFailure(err), ErrProxyTimeout) {
			if timeoutOrdinal == 0 {
				timeoutOrdinal = attempted
				timeoutNative = lastNative + " (context=" + contextState + ")"
			}
			category = ErrProxyTimeout
		}
		if errors.Is(failure, ErrProxyTimeout) {
			category = ErrProxyTimeout
		}
		failure = category
	}
	if err := ctx.Err(); err != nil {
		stopReason = "total_or_parent_context"
		contextState = diagnostics.Text(err.Error(), 64)
		return nil, proxyTransportFailure(err)
	}
	return nil, failure
}

// ProxyExchange makes one fresh HTTP/1 upstream attempt. No pool, coalescing,
// redirect client or GetBody exists; the single-use dial also fences hidden
// transport redials. Client-facing intercepted H2 is independent of this hop.
func (p *ProxyAddress) ProxyExchange(ctx context.Context, target httppolicy.Request, header http.Header, body io.ReadCloser, length int64, private bool, roots *x509.CertPool) (*http.Response, error) {
	if p == nil || target.Destination() != p.destination || ValidateProxyHeaders(header) != nil {
		return nil, ErrInvalidURL
	}
	outgoing := header.Clone()
	outgoing.Set("Host", target.Destination().Authority())
	outgoing.Set("Connection", "close")
	// Budget transport-generated fields as well as the supplied header map.
	if body != nil {
		if length >= 0 {
			outgoing.Del("Transfer-Encoding")
			outgoing.Set("Content-Length", strconv.FormatInt(length, 10))
		} else {
			outgoing.Del("Content-Length")
			outgoing.Set("Transfer-Encoding", "chunked") //nolint:gosec // Budget-only map, never transmitted; mutually exclusive framing is generated by Transport.
		}
	}
	if _, present := outgoing["User-Agent"]; !present {
		outgoing.Set("User-Agent", "Go-http-client/1.1")
	}
	if ValidateProxyHeaders(outgoing) != nil {
		return nil, ErrResponseLimit
	}
	// Establish in the request owner: net/http detaches its dial context from
	// request cancellation and may return before that dial settles. No candidate
	// owner may outlive this exchange's connection-establishment phase.
	conn, err := p.Dial(ctx, private)
	if err != nil {
		if body != nil {
			_ = body.Close()
		}
		return nil, err
	}
	var handedOff atomic.Bool
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport := &http.Transport{Protocols: protocols, Proxy: nil, DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false, MaxResponseHeaderBytes: contract.HTTPProxyHeaderBytes, TLSHandshakeTimeout: contract.HTTPProxyDialTimeout, ResponseHeaderTimeout: contract.HTTPProxyHeaderTimeout,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if !handedOff.CompareAndSwap(false, true) {
				return nil, ErrAddressPolicy
			}
			return conn, nil
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.destination.Host(), RootCAs: roots}, //nolint:gosec // Verification remains enabled, with platform roots in production.
	}
	req := &http.Request{Method: target.Method(), URL: target.URL(), Host: target.Destination().Authority(), Header: header.Clone(), Body: body, ContentLength: length, Close: true}
	requestCtx, cancel := context.WithCancel(ctx)
	req = req.WithContext(requestCtx)
	response, err := transport.RoundTrip(req)
	if err != nil {
		cancel()
		_ = conn.Close()
		transport.CloseIdleConnections()
		if errors.Is(err, ErrAddressPolicy) {
			return nil, ErrAddressPolicy
		}
		category := proxyTransportFailure(err)
		if errors.Is(err, ErrProxyTimeout) {
			category = ErrProxyTimeout
		}
		return nil, diagnostics.WithDetail(category, diagnostics.Snapshot("remote", "exchange", p.destination.Authority(), err, diagnostics.HTTPSecrets(header)...))
	}
	if response.StatusCode == http.StatusSwitchingProtocols || ValidateProxyHeaders(response.Header) != nil {
		cancel()
		_ = response.Body.Close()
		transport.CloseIdleConnections()
		return nil, ErrResponseLimit
	}
	response.Body = &responseBody{ReadCloser: response.Body, transport: transport, cancel: cancel}
	return response, nil
}

func ValidateProxyHeaders(header http.Header) error { return validateHeaders(header) }

type proxyIdleConn struct{ net.Conn }

func (c *proxyIdleConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return c.Close()
}

func (c *proxyIdleConn) Read(b []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(contract.HTTPProxyIdleTimeout)); err != nil {
		return 0, errors.Join(ErrProxyDeadline, proxyTransportFailure(err))
	}
	return c.Conn.Read(b)
}
func (c *proxyIdleConn) Write(b []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(contract.HTTPProxyIdleTimeout)); err != nil {
		return 0, errors.Join(ErrProxyDeadline, proxyTransportFailure(err))
	}
	return c.Conn.Write(b)
}
