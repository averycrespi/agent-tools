package httpboundary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

type AuthenticateFunc func(context.Context, *http.Request, contract.CredentialAuthority) (context.Context, error)

type AuthenticationCleanup interface {
	Release()
}

type authenticationCleanupKey struct{}

func WithAuthenticationCleanup(ctx context.Context, cleanup AuthenticationCleanup) context.Context {
	return context.WithValue(ctx, authenticationCleanupKey{}, cleanup)
}

func releaseAuthentication(ctx context.Context) {
	if cleanup, ok := ctx.Value(authenticationCleanupKey{}).(AuthenticationCleanup); ok && cleanup != nil {
		cleanup.Release()
	}
}

type Options struct {
	Diagnostics          diagnostics.HTTPProxyObserver
	AllowedHosts         []string
	Authority            string
	ExternalOrigin       string
	Ready                func() bool
	Draining             func() bool
	Authenticate         AuthenticateFunc
	AuthenticatedProblem func(*http.Request, contract.ProblemCode) contract.ProblemCode
	Next                 http.Handler
}

type Boundary struct {
	diagnostics          diagnostics.HTTPProxyObserver
	allowedHosts         map[string]struct{}
	authority            string
	origin               string
	publicAuthority      string
	localAuthority       string
	ready                func() bool
	draining             func() bool
	authenticate         AuthenticateFunc
	authenticatedProblem func(*http.Request, contract.ProblemCode) contract.ProblemCode
	next                 http.Handler
	regular              chan struct{}
	control              chan struct{}
	admin                chan struct{}
	health               chan struct{}
}

type Error struct {
	Code      contract.ProblemCode
	SetCookie string
}

func (failure Error) Error() string { return string(failure.Code) }

func ValidateAuthority(authority string) error {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("authority must be a numeric IPv4 address and port")
	}
	address := net.ParseIP(host)
	if address == nil || address.To4() == nil || address.IsMulticast() || strings.Contains(host, ":") {
		return fmt.Errorf("authority must use a numeric unicast or wildcard IPv4 address")
	}
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return fmt.Errorf("authority port must be numeric and nonzero")
	}
	if net.JoinHostPort(host, strconv.FormatUint(value, 10)) != authority {
		return fmt.Errorf("authority must be canonical")
	}
	return nil
}

func New(options Options) (*Boundary, error) {
	if options.Authority == "" {
		options.Authority = contract.DefaultAuthority
	}
	origin, err := BrowserOrigin(options.Authority, options.ExternalOrigin)
	if err != nil {
		return nil, err
	}
	publicAuthority := ""
	if options.ExternalOrigin != "" {
		publicAuthority = strings.TrimPrefix(origin, "https://")
	}
	allowedHosts := make(map[string]struct{}, len(options.AllowedHosts))
	for _, host := range options.AllowedHosts {
		normalized, ok := contract.NormalizeHostname(host)
		if !ok {
			return nil, fmt.Errorf("allowed host must be an ASCII DNS hostname without a port or trailing dot")
		}
		allowedHosts[normalized] = struct{}{}
	}
	if options.Ready == nil {
		options.Ready = func() bool { return false }
	}
	if options.Next == nil {
		options.Next = http.NotFoundHandler()
	}
	if options.Draining == nil {
		options.Draining = func() bool { return false }
	}
	return &Boundary{
		diagnostics:          options.Diagnostics,
		allowedHosts:         allowedHosts,
		authority:            options.Authority,
		origin:               origin,
		publicAuthority:      publicAuthority,
		localAuthority:       LocalAuthority(options.Authority),
		ready:                options.Ready,
		draining:             options.Draining,
		authenticate:         options.Authenticate,
		authenticatedProblem: options.AuthenticatedProblem,
		next:                 options.Next,
		regular:              makePermit("http_regular"),
		control:              makePermit("http_control_auth"),
		admin:                makePermit("http_admin"),
		health:               makePermit("http_health"),
	}, nil
}

func (boundary *Boundary) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	operation := "request"
	defer func() {
		if recovered := recover(); recovered != nil {
			err, _ := recovered.(error)
			if !errors.Is(err, http.ErrAbortHandler) && boundary.diagnostics != nil {
				// API/MCP bodies may carry arbitrary secrets. Without source-owned
				// masking material, retain the location/stack but not panic values.
				detail := diagnostics.Panic("http boundary", operation, boundary.authority, "panic detail withheld: request may contain sensitive payload")
				boundary.diagnostics.HTTPProxy(diagnostics.Facts{Event: diagnostics.OperatorFailure, Detail: detail})
			}
			// Preserve net/http's existing connection-abort behavior, without
			// letting its library logger serialize the original panic value.
			panic(http.ErrAbortHandler)
		}
	}()
	if strings.HasPrefix(request.URL.Path, "/api/v2/") || strings.HasPrefix(request.URL.Path, "/api/v1/") {
		writer.Header().Set("Cache-Control", "no-store")
	}
	if code := boundary.validateEarly(request); code != "" {
		writeProblem(writer, code)
		return
	}
	route, known := contract.RouteForPath(request.URL.Path)
	if !known {
		writeProblem(writer, contract.ProblemNotFound)
		return
	}
	operation = route.Pattern
	if !methodAllowed(route, request.Method) {
		writer.Header().Set("Allow", route.Allow())
		writeProblem(writer, contract.ProblemMethodNotAllowed)
		return
	}

	// Retirement is transport-only: the legacy cookie never selects authority.
	// Run after early validation, but before authentication can reject old browsers.
	if request.Header.Get("Origin") == boundary.origin &&
		(route.Pattern == "/api/v2/admin-sessions" || route.Pattern == "/api/v2/admin-sessions/current") {
		if _, err := request.Cookie(contract.LegacySessionCookieName); err == nil {
			http.SetCookie(writer, &http.Cookie{ //nolint:gosec // Secure follows configured origin; only local plain-loopback intentionally omits it.
				Name: contract.LegacySessionCookieName, Path: "/", MaxAge: -1,
				Expires: time.Unix(1, 0).UTC(), HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: boundary.publicAuthority != "",
			})
		}
	}

	authority := contract.AuthorityForMethod(route, request.Method)
	permit := boundary.permitFor(route, authority)
	if !tryAcquire(permit) {
		writeProblem(writer, contract.ProblemResourceLimit)
		return
	}
	defer func() {
		if permit != nil {
			release(permit)
		}
	}()

	if request.URL.Path == "/livez" {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "live"})
		return
	}
	if request.URL.Path == "/readyz" {
		if boundary.ready() {
			writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
		} else {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		}
		return
	}
	ctx := request.Context()
	if requiresAuthentication(authority) {
		if boundary.authenticate == nil {
			writeProblem(writer, contract.ProblemAuthenticationRequired)
			return
		}
		authenticated, err := boundary.authenticate(ctx, request, authority)
		if err != nil {
			var failure Error
			if errors.As(err, &failure) {
				if failure.SetCookie != "" {
					writer.Header().Add("Set-Cookie", failure.SetCookie)
				}
				writeProblem(writer, failure.Code)
			} else {
				writeProblem(writer, contract.ProblemAuthenticationRequired)
			}
			return
		}
		ctx = authenticated
		request = request.WithContext(ctx)
		defer releaseAuthentication(ctx)
		if isAdmin(authority) {
			if !tryAcquire(boundary.admin) {
				writeProblem(writer, boundary.admitAuthenticatedProblem(request, contract.ProblemResourceLimit))
				return
			}
			release(permit)
			permit = boundary.admin
			if route.Pattern == "/api/v2/events" {
				release(permit)
				permit = nil
			}
		}
	}
	if boundary.draining() && request.URL.Path != "/api/v2/system-status" {
		code := contract.ProblemShuttingDown
		if isAdmin(authority) {
			code = boundary.admitAuthenticatedProblem(request, code)
		}
		writeProblem(writer, code)
		return
	}
	boundary.next.ServeHTTP(writer, request.WithContext(ctx))
}

func (boundary *Boundary) admitAuthenticatedProblem(request *http.Request, code contract.ProblemCode) contract.ProblemCode {
	if boundary.authenticatedProblem != nil {
		return boundary.authenticatedProblem(request, code)
	}
	return code
}

func (boundary *Boundary) validateEarly(request *http.Request) contract.ProblemCode {
	target := request.RequestURI
	if target == "" {
		target = request.URL.RequestURI()
	}
	if len(target) > limit("request_target_bytes") || request.URL.IsAbs() || strings.HasPrefix(target, "//") {
		return contract.ProblemMalformedRequest
	}
	if !boundary.acceptsRequestHost(request) {
		return contract.ProblemMisdirectedRequest
	}
	if request.URL.Path == "/oauth/callback" {
		if request.URL.RawPath != "" || request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
			return contract.ProblemMalformedRequest
		}
	}
	count, total := 0, 0
	for name, values := range request.Header {
		lower := strings.ToLower(name)
		if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") {
			return contract.ProblemMalformedRequest
		}
		count += len(values)
		total += len(name)
		for _, value := range values {
			if len(value) > limit("request_header_value_bytes") {
				return contract.ProblemMalformedRequest
			}
			total += len(value)
		}
	}
	if count > limit("request_header_count") || total > limit("request_header_bytes") {
		return contract.ProblemMalformedRequest
	}
	origins := request.Header.Values("Origin")
	if boundary.publicAuthority != "" && strings.HasPrefix(request.URL.Path, "/api/v2/admin-sessions") && len(origins) == 0 {
		return contract.ProblemForbiddenOrigin
	}
	if len(origins) > 1 || (len(origins) == 1 && origins[0] != boundary.origin) {
		return contract.ProblemForbiddenOrigin
	}
	return ""
}

// Remote-mode internal hosts are only transport reachability: they cannot
// exchange or use browser cookies, even with a forged public Origin.
func (boundary *Boundary) acceptsRequestHost(request *http.Request) bool {
	if boundary.publicAuthority == "" {
		return boundary.acceptsHost(request.Host)
	}
	if request.Host == boundary.publicAuthority {
		return true
	}
	publicURL := url.URL{Host: boundary.publicAuthority}
	requestURL := url.URL{Host: request.Host}
	if request.Host != boundary.localAuthority && strings.EqualFold(requestURL.Hostname(), publicURL.Hostname()) {
		return false
	}
	if request.Host != boundary.localAuthority && !boundary.acceptsHost(request.Host) {
		return false
	}
	if len(request.Header.Values("Origin")) != 0 || len(request.Header.Values("Cookie")) != 0 {
		return false
	}
	if request.URL.Path == "/livez" || request.URL.Path == "/readyz" {
		return true
	}
	if strings.HasPrefix(request.URL.Path, "/api/v2/admin-sessions") {
		return false
	}
	route, known := contract.RouteForPath(request.URL.Path)
	return known && requiresAuthentication(contract.AuthorityForMethod(route, request.Method))
}

func (boundary *Boundary) acceptsHost(authority string) bool {
	if authority == boundary.authority && !strings.HasPrefix(authority, "0.0.0.0:") {
		return true
	}
	host := authority
	if strings.Contains(authority, ":") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil || port == "" || strings.Trim(port, "0123456789") != "" {
			return false
		}
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 || strings.ContainsAny(authority, "[]") {
			return false
		}
	}
	normalized, ok := contract.NormalizeHostname(host)
	if !ok {
		return false
	}
	_, ok = boundary.allowedHosts[normalized]
	return ok
}

func (boundary *Boundary) AdmissionStatus() (regular, control, admin, health contract.LimitStatus) {
	return permitStatus(boundary.regular), permitStatus(boundary.control), permitStatus(boundary.admin), permitStatus(boundary.health)
}

func permitStatus(permit chan struct{}) contract.LimitStatus {
	inUse, maximum := int64(len(permit)), int64(cap(permit))
	return contract.LimitStatus{InUse: inUse, Limit: maximum, Saturated: inUse >= maximum}
}

func (boundary *Boundary) permitFor(route contract.Route, authority contract.CredentialAuthority) chan struct{} {
	if route.Pattern == "/livez" || route.Pattern == "/readyz" {
		return boundary.health
	}
	if isAdmin(authority) {
		return boundary.control
	}
	return boundary.regular
}

func makePermit(name string) chan struct{} {
	maximum := limit(name)
	return make(chan struct{}, maximum)
}

func limit(name string) int {
	value, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing contract limit: " + name)
	}
	return int(value.Maximum)
}

func tryAcquire(permit chan struct{}) bool {
	select {
	case permit <- struct{}{}:
		return true
	default:
		return false
	}
}

func release(permit chan struct{}) { <-permit }

func requiresAuthentication(authority contract.CredentialAuthority) bool {
	return authority == contract.AuthorityAgent || isAdmin(authority)
}

func isAdmin(authority contract.CredentialAuthority) bool {
	return authority == contract.AuthorityAdmin || authority == contract.AuthorityAdminBearer || authority == contract.AuthorityAdminSession
}

func methodAllowed(route contract.Route, method string) bool {
	for _, allowed := range route.Methods {
		if method == allowed {
			return true
		}
	}
	return false
}

func writeProblem(writer http.ResponseWriter, code contract.ProblemCode) {
	if code == contract.ProblemAuthenticationRequired {
		writer.Header().Set("WWW-Authenticate", "Bearer")
	}
	problem, ok := contract.ProblemForCode(code)
	if !ok {
		problem, _ = contract.ProblemForCode(contract.ProblemMalformedRequest)
	}
	writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
	writer.WriteHeader(problem.Status)
	_ = json.NewEncoder(writer).Encode(contract.ProblemEnvelope(problem))
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", contract.MediaTypeJSON)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
