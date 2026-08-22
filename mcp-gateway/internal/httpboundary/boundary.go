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

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type AuthenticateFunc func(context.Context, *http.Request, contract.CredentialAuthority) (context.Context, error)

type Options struct {
	Authority    string
	Ready        func() bool
	Authenticate AuthenticateFunc
	Next         http.Handler
}

type Boundary struct {
	authority    string
	origin       string
	ready        func() bool
	authenticate AuthenticateFunc
	next         http.Handler
	regular      chan struct{}
	control      chan struct{}
	admin        chan struct{}
	health       chan struct{}
}

type Error struct {
	Code contract.ProblemCode
}

func (failure Error) Error() string { return string(failure.Code) }

func ValidateAuthority(authority string) error {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("authority must be a numeric IPv4 loopback address and port")
	}
	address := net.ParseIP(host)
	if address == nil || address.To4() == nil || !address.IsLoopback() || strings.Contains(host, ":") {
		return fmt.Errorf("authority must use IPv4 loopback")
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
	if err := ValidateAuthority(options.Authority); err != nil {
		return nil, err
	}
	if options.Ready == nil {
		options.Ready = func() bool { return false }
	}
	if options.Next == nil {
		options.Next = http.NotFoundHandler()
	}
	return &Boundary{
		authority:    options.Authority,
		origin:       "http://" + options.Authority,
		ready:        options.Ready,
		authenticate: options.Authenticate,
		next:         options.Next,
		regular:      makePermit("http_regular"),
		control:      makePermit("http_control_auth"),
		admin:        makePermit("http_admin"),
		health:       makePermit("http_health"),
	}, nil
}

func (boundary *Boundary) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/api/v1/") {
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
	if !methodAllowed(route, request.Method) {
		writer.Header().Set("Allow", route.Allow())
		writeProblem(writer, contract.ProblemMethodNotAllowed)
		return
	}

	permit := boundary.permitFor(route)
	if !tryAcquire(permit) {
		writeProblem(writer, contract.ProblemResourceLimit)
		return
	}
	defer func() { release(permit) }()

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
	if requiresAuthentication(route.Authority) {
		if boundary.authenticate == nil {
			writeProblem(writer, contract.ProblemAuthenticationRequired)
			return
		}
		authenticated, err := boundary.authenticate(ctx, request, route.Authority)
		if err != nil {
			var failure Error
			if errors.As(err, &failure) {
				writeProblem(writer, failure.Code)
			} else {
				writeProblem(writer, contract.ProblemAuthenticationRequired)
			}
			return
		}
		ctx = authenticated
		if isAdmin(route.Authority) {
			if !tryAcquire(boundary.admin) {
				writeProblem(writer, contract.ProblemResourceLimit)
				return
			}
			release(permit)
			permit = boundary.admin
		}
	}
	boundary.next.ServeHTTP(writer, request.WithContext(ctx))
}

func (boundary *Boundary) validateEarly(request *http.Request) contract.ProblemCode {
	target := request.RequestURI
	if target == "" {
		target = request.URL.RequestURI()
	}
	if len(target) > limit("request_target_bytes") || request.URL.IsAbs() || strings.HasPrefix(target, "//") {
		return contract.ProblemMalformedRequest
	}
	if strings.ToLower(request.Host) != boundary.authority {
		return contract.ProblemMisdirectedRequest
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
	if origin := request.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.String() != boundary.origin {
			return contract.ProblemForbiddenOrigin
		}
	}
	return ""
}

func (boundary *Boundary) AdmissionStatus() (regular, control, admin, health contract.LimitStatus) {
	return permitStatus(boundary.regular), permitStatus(boundary.control), permitStatus(boundary.admin), permitStatus(boundary.health)
}

func permitStatus(permit chan struct{}) contract.LimitStatus {
	inUse, maximum := int64(len(permit)), int64(cap(permit))
	return contract.LimitStatus{InUse: inUse, Limit: maximum, Saturated: inUse >= maximum}
}

func (boundary *Boundary) permitFor(route contract.Route) chan struct{} {
	if route.Pattern == "/livez" || route.Pattern == "/readyz" {
		return boundary.health
	}
	if isAdmin(route.Authority) {
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
