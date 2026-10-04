package contract

// HTTPProxyFailureReason is the closed RFC 9209 error vocabulary used only for
// Gateway-generated responses. It never describes an upstream application status.
func HTTPProxyFailureReason(status int) string {
	switch status {
	case 400:
		return "http_request_error"
	case 407:
		return "http_request_denied"
	case 403:
		return "http_request_denied"
	case 429:
		return "connection_limit_reached"
	case 503:
		return "proxy_internal_error"
	case 504:
		return "connection_timeout"
	default:
		return "connection_terminated"
	}
}

const (
	HTTPProxyCorrelationHeader = "Gateway-Request-ID"
	HTTPProxyConnectionHeader  = "Gateway-Connection-ID"
)
